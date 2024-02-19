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

package api

import (
	"context"
	"fmt"
	"io"

	"github.com/harness/gitness/errors"
)

type BlobReader struct {
	SHA string
	// Size is the actual size of the blob.
	Size int64
	// ContentSize is the total number of bytes returned by the Content Reader.
	ContentSize int64
	// Content contains the (partial) content of the blob.
	Content io.ReadCloser
}

// GetBlob returns the blob for the given object sha.
func (g *Git) GetBlob(
	ctx context.Context,
	repoPath string,
	sha string,
	sizeLimit int64,
) (*BlobReader, error) {
	stdIn, stdOut, cancel := CatFileBatch(ctx, repoPath)

	_, err := stdIn.Write([]byte(sha + "\n"))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to write blob sha to git stdin: %w", err)
	}

	objectSHA, objectType, objectSize, err := ReadBatchHeaderLine(stdOut)
	if err != nil {
		cancel()
		return nil, processGitErrorf(err, "failed to read cat-file batch line")
	}

	if string(objectSHA) != sha {
		cancel()
		return nil, fmt.Errorf("cat-file returned object sha '%s' but expected '%s'", objectSHA, sha)
	}
	if objectType != string(GitObjectTypeBlob) {
		cancel()
		return nil, errors.InvalidArgument(
			"cat-file returned object type '%s' but expected '%s'", objectType, GitObjectTypeBlob)
	}

	contentSize := objectSize
	if sizeLimit > 0 && sizeLimit < contentSize {
		contentSize = sizeLimit
	}

	return &BlobReader{
		SHA:         sha,
		Size:        objectSize,
		ContentSize: contentSize,
		Content:     newLimitReaderCloser(stdOut, contentSize, cancel),
	}, nil
}

func newLimitReaderCloser(reader io.Reader, limit int64, stop func()) limitReaderCloser {
	return limitReaderCloser{
		reader: io.LimitReader(reader, limit),
		stop:   stop,
	}
}

type limitReaderCloser struct {
	reader io.Reader
	stop   func()
}

func (l limitReaderCloser) Read(p []byte) (n int, err error) {
	return l.reader.Read(p)
}

func (l limitReaderCloser) Close() error {
	l.stop()
	return nil
}
