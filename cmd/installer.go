package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

func retrieveInstaller(
	ctx context.Context,
	host Host,
	target InstallTarget,
	address string,
	tempPattern string,
	sizeLimit int64,
) (TemporaryArtifact, error) {
	response, err := host.Retrieve(ctx, address)
	if err != nil {
		return TemporaryArtifact{}, fmt.Errorf("download %s installer: %w", target, err)
	}
	if response.Body == nil {
		return TemporaryArtifact{}, fmt.Errorf("download %s installer: empty response body", target)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return TemporaryArtifact{}, fmt.Errorf("download %s installer: unexpected HTTP status %d", target, response.StatusCode)
	}
	finalURL, err := url.Parse(response.FinalURL)
	if err != nil || !strings.EqualFold(finalURL.Scheme, "https") {
		return TemporaryArtifact{}, fmt.Errorf("download %s installer: redirect did not remain on HTTPS", target)
	}

	artifact, err := host.CreateTemp(tempPattern)
	if err != nil {
		return TemporaryArtifact{}, fmt.Errorf("create temporary %s installer: %w", target, err)
	}
	if err := fillTemporaryArtifact(artifact, response.Body, sizeLimit); err != nil {
		result := fmt.Errorf("prepare temporary %s installer: %w", target, err)
		finishTemporaryArtifact(artifact, string(target)+" installer", &result)
		return TemporaryArtifact{}, result
	}
	return artifact, nil
}

func fillTemporaryArtifact(artifact TemporaryArtifact, body io.Reader, sizeLimit int64) error {
	if artifact.File == nil {
		return errors.New("no file returned")
	}

	limited := io.LimitReader(body, sizeLimit+1)
	written, err := io.Copy(artifact.File, limited)
	if err != nil {
		_ = artifact.File.Close()
		return fmt.Errorf("write file: %w", err)
	}
	if written > sizeLimit {
		_ = artifact.File.Close()
		return fmt.Errorf("response exceeds %d bytes", sizeLimit)
	}
	if err := artifact.File.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}
	return nil
}

func removeTemporaryArtifact(artifact TemporaryArtifact) error {
	if artifact.Remove == nil {
		return nil
	}
	return artifact.Remove()
}

func finishTemporaryArtifact(artifact TemporaryArtifact, description string, result *error) {
	if err := removeTemporaryArtifact(artifact); err != nil {
		cleanupError := fmt.Errorf("remove temporary %s: %w", description, err)
		if *result == nil {
			*result = cleanupError
		} else {
			*result = fmt.Errorf("%v; %w", *result, cleanupError)
		}
	}
}
