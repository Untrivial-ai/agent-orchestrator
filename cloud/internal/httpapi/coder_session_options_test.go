package httpapi

import "testing"

func TestParseCoderConfigInput(t *testing.T) {
	t.Parallel()
	const goodTemplate = "2a2e262c-b31c-4202-946d-a19ad45d1fd2"

	t.Run("empty config is inert", func(t *testing.T) {
		cfg, err := parseCoderConfigInput(&coderConfigInput{})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TemplateID != "" || cfg.Size != "" || cfg.StartupScript != "" || len(cfg.ExtraRepos) != 0 {
			t.Fatalf("empty config not empty: %+v", cfg)
		}
	})

	t.Run("template + size + startup + repo", func(t *testing.T) {
		cfg, err := parseCoderConfigInput(&coderConfigInput{
			TemplateID:    goodTemplate,
			Size:          "Large",
			StartupScript: "make dev",
			ExtraRepos:    []createSessionRepo{{URL: "https://github.com/acme/lib.git", Branch: "main"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TemplateID != goodTemplate || cfg.Size != "large" || cfg.StartupScript != "make dev" {
			t.Fatalf("config = %+v", cfg)
		}
		if len(cfg.ExtraRepos) != 1 || cfg.ExtraRepos[0].URL != "https://github.com/acme/lib" || cfg.ExtraRepos[0].Branch != "main" {
			t.Fatalf("repos = %+v", cfg.ExtraRepos)
		}
	})

	t.Run("bad template UUID is rejected", func(t *testing.T) {
		if _, err := parseCoderConfigInput(&coderConfigInput{TemplateID: "not-a-uuid"}); err == nil {
			t.Fatal("expected error for bad template UUID")
		}
	})

	t.Run("bad size is rejected", func(t *testing.T) {
		if _, err := parseCoderConfigInput(&coderConfigInput{TemplateID: goodTemplate, Size: "huge"}); err == nil {
			t.Fatal("expected error for bad size")
		}
	})

	t.Run("size without a template is rejected", func(t *testing.T) {
		if _, err := parseCoderConfigInput(&coderConfigInput{Size: "large"}); err == nil {
			t.Fatal("expected error: size requires a chosen template")
		}
	})

	t.Run("non-github extra repo is rejected", func(t *testing.T) {
		if _, err := parseCoderConfigInput(&coderConfigInput{
			ExtraRepos: []createSessionRepo{{URL: "https://gitlab.com/a/b"}},
		}); err == nil {
			t.Fatal("expected error for non-github extra repo")
		}
	})
}
