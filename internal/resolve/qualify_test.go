package resolve

import (
	"net/http"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// noLocalImages is a server on which no image or alias exists: every name is, as far as the live server goes, not a local image.
type noLocalImages struct{ incus.InstanceServer }

func (s noLocalImages) UseProject(string) incus.InstanceServer { return s }
func (s noLocalImages) GetImageAlias(string) (*api.ImageAliasesEntry, string, error) {
	return nil, "", api.StatusErrorf(http.StatusNotFound, "not found")
}
func (s noLocalImages) GetImage(string) (*api.Image, string, error) {
	return nil, "", api.StatusErrorf(http.StatusNotFound, "not found")
}

func TestStackImagesFollowTheSameRegistryRulesAsRun(t *testing.T) {
	rs := []Resource{
		{Kind: KindInstance, Name: "kuma", Image: "louislam/uptime-kuma:2"},
		{Kind: KindInstance, Name: "abs", Image: "ghcr.io/advplyr/audiobookshelf:latest"},
		{Kind: KindInstance, Name: "pinned", Image: "docker-oci:library/redis:7@sha256:abc"},
		{Kind: KindStorageVolume, Name: "louislam/not-an-image"},
	}
	lines, err := QualifyImages(noLocalImages{}, rs)
	if err != nil {
		t.Fatal(err)
	}
	if rs[0].Image != "docker-oci:louislam/uptime-kuma:2" || rs[1].Image != "ghcr:advplyr/audiobookshelf:latest" {
		t.Errorf("images = %q, %q", rs[0].Image, rs[1].Image)
	}
	if rs[2].Image != "docker-oci:library/redis:7@sha256:abc" {
		t.Errorf("an explicit reference must be left exactly as written: %q", rs[2].Image)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "warning: instance/kuma: ") || !strings.Contains(joined, "names no registry") {
		t.Errorf("a bare reference gets a warning that names the instance:\n%s", joined)
	}
	if strings.Contains(joined, "instance/abs") || strings.Contains(joined, "instance/pinned") {
		t.Errorf("an explicit reference (a registry host, or REMOTE:REF) gets no message:\n%s", joined)
	}
}

func TestAnImageTheStackItselfDeclaresIsNotARegistryReference(t *testing.T) {
	rs := []Resource{
		{Kind: KindImage, Name: "haos-image", Alias: "haos-x86-64"},
		{Kind: KindInstance, Name: "haos-by-name", Image: "haos-image"},
		{Kind: KindInstance, Name: "haos-by-alias", Image: "haos-x86-64"},
		{Kind: KindImage, Name: "u", Alias: "team/custom:1"},
		{Kind: KindInstance, Name: "slashed", Image: "team/custom:1"},
	}
	lines, err := QualifyImages(noLocalImages{}, rs)
	if err != nil || len(lines) != 0 {
		t.Fatalf("%v %v", lines, err)
	}
	for _, r := range rs[1:] {
		if strings.HasPrefix(r.Image, "docker-oci:") {
			t.Errorf("%s: a name the stack declares was sent to Docker Hub: %q", r.Name, r.Image)
		}
	}
}

func TestAnUnknownRegistryStopsThePlanNamingTheInstance(t *testing.T) {
	_, err := QualifyImages(noLocalImages{}, []Resource{{Kind: KindInstance, Name: "app", Image: "registry.example.com/team/app:1"}})
	if err == nil || !strings.Contains(err.Error(), "instance/app") || !strings.Contains(err.Error(), "incus remote add") {
		t.Errorf("err = %v", err)
	}
}
