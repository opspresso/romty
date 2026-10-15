// Package update asks GitHub for the newest romty release and decides whether
// it is newer than the running one.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// LatestReleaseURL is the GitHub API endpoint for the newest published
// release. It skips drafts and prereleases.
const LatestReleaseURL = "https://api.github.com/repos/opspresso/romty/releases/latest"

// maxResponseBytes bounds the body read from the API. A release document is a
// few kilobytes; anything far larger is not one.
const maxResponseBytes = 1 << 20

// LatestRelease returns the tag of the newest published romty release.
func LatestRelease(ctx context.Context) (string, error) {
	return Latest(ctx, http.DefaultClient, LatestReleaseURL)
}

// Latest returns the tag of the newest release published at url.
func Latest(ctx context.Context, client *http.Client, url string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("latest release: %s", response.Status)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&release); err != nil {
		return "", fmt.Errorf("latest release: %w", err)
	}
	if _, ok := parse(release.TagName); !ok {
		return "", fmt.Errorf("latest release: unexpected tag %q", release.TagName)
	}
	return release.TagName, nil
}

// Newer reports whether latest is a later release than current. A version
// that is not semantic never counts as newer, so a malformed tag cannot
// announce an update.
func Newer(current, latest string) bool {
	a, ok := parse(current)
	if !ok {
		return false
	}
	b, ok := parse(latest)
	if !ok {
		return false
	}
	for index := range a.numbers {
		if a.numbers[index] != b.numbers[index] {
			return b.numbers[index] > a.numbers[index]
		}
	}
	// A prerelease comes before the release it leads to.
	return a.prerelease && !b.prerelease
}

type semantic struct {
	numbers    [3]int
	prerelease bool
}

func parse(value string) (semantic, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	value, _, _ = strings.Cut(value, "+")
	core, pre, hasPre := strings.Cut(value, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 || hasPre && pre == "" {
		return semantic{}, false
	}
	var result semantic
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 || part != strconv.Itoa(number) {
			return semantic{}, false
		}
		result.numbers[index] = number
	}
	result.prerelease = hasPre
	return result, true
}
