// Copyright 2023 Harness, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package githook

import (
	"context"
	"fmt"

	"github.com/harness/gitness/app/services/settings"
	"github.com/harness/gitness/git"
	"github.com/harness/gitness/git/api"
	"github.com/harness/gitness/git/hook"
	"github.com/harness/gitness/logging"
	"github.com/harness/gitness/types"

	"github.com/gotidy/ptr"
	"github.com/rs/zerolog/log"
)

type scanSecretsResult struct {
	findings []api.Finding
}

func (r *scanSecretsResult) HasResults() bool {
	return len(r.findings) > 0
}

func (c *Controller) scanSecrets(
	ctx context.Context,
	rgit RestrictedGIT,
	repo *types.Repository,
	in types.GithookPreReceiveInput,
	output *hook.Output,
) error {
	// check if scanning is enabled on the repo
	scanningEnabled, err := settings.RepoGet(
		ctx,
		c.settings,
		repo.ID,
		settings.KeySecretScanningEnabled,
		settings.DefaultSecretScanningEnabled,
	)
	if err != nil {
		return fmt.Errorf("failed to check settings whether secret scanning is enabled: %w", err)
	}
	if !scanningEnabled {
		return nil
	}

	// scan for secrets
	scanResult, err := scanSecretsInternal(
		ctx,
		rgit,
		repo,
		in,
	)
	if err != nil {
		return fmt.Errorf("failed to scan for git leaks: %w", err)
	}

	if !scanResult.HasResults() {
		return nil
	}

	// pretty print output
	printScanSecretsFindings(output, scanResult.findings)
	output.Messages = append(output.Messages, "", "")
	output.Error = ptr.String("Changes blocked by security scan results")

	return nil
}

func scanSecretsInternal(ctx context.Context,
	rgit RestrictedGIT,
	repo *types.Repository,
	in types.GithookPreReceiveInput,
) (scanSecretsResult, error) {
	var baseRevFallBack *string
	res := scanSecretsResult{}

	for _, refUpdate := range in.RefUpdates {
		ctx := logging.NewContext(ctx, loggingWithRefUpdate(refUpdate))
		log := log.Ctx(ctx)

		if refUpdate.New.String() == types.NilSHA {
			log.Debug().Msg("skip deleted reference")
			continue
		}

		// in case the branch was just created - fallback to compare against latest default branch.
		baseRev := refUpdate.Old.String() + "^{commit}"
		rev := refUpdate.New.String() + "^{commit}"
		//nolint:nestif
		if refUpdate.Old.IsNil() {
			if baseRevFallBack == nil {
				fallbackSHA, fallbackAvailable, err := GetBaseSHAForScanningChanges(
					ctx,
					rgit,
					repo,
					in.Environment,
					in.RefUpdates,
					refUpdate,
				)
				if err != nil {
					return scanSecretsResult{}, fmt.Errorf("failed to get fallback sha: %w", err)
				}

				if fallbackAvailable {
					log.Debug().Msgf("found fallback sha %q", fallbackSHA)
					baseRevFallBack = ptr.String(fallbackSHA.String())
				} else {
					log.Debug().Msg("no fallback sha available, do full scan instead")
					baseRevFallBack = ptr.String("")
				}
			}

			log.Debug().Msgf("new reference, use rev %q as base for secret scanning", *baseRevFallBack)

			baseRev = *baseRevFallBack
		}

		log.Debug().Msg("scan for secrets")

		scanSecretsOut, err := rgit.ScanSecrets(ctx, &git.ScanSecretsParams{
			ReadParams: git.ReadParams{
				RepoUID:             repo.GitUID,
				AlternateObjectDirs: in.Environment.AlternateObjectDirs,
			},
			BaseRev: baseRev,
			Rev:     rev,
		})
		if err != nil {
			return scanSecretsResult{}, fmt.Errorf("failed to detect secret leaks: %w", err)
		}

		if len(scanSecretsOut.Findings) == 0 {
			log.Debug().Msg("no new secrets found")
			continue
		}

		log.Debug().Msgf("found %d new secrets", len(scanSecretsOut.Findings))

		res.findings = append(res.findings, scanSecretsOut.Findings...)
	}

	return res, nil
}
