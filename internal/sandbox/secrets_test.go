package sandbox

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCreateSessionSecrets_SkipsUnsetNames(t *testing.T) {
	t.Setenv("YC_TEST_UNSET_VAR_XYZ", "present")
	os.Unsetenv("YC_TEST_UNSET_VAR_XYZ")

	origCreate := podmanSecretCreate
	defer func() { podmanSecretCreate = origCreate }()
	podmanSecretCreate = func(context.Context, string, string, []string) error {
		t.Fatal("podman secret create must not be called for an unset env var")
		return nil
	}

	mounts, err := createSessionSecrets(context.Background(), "yc-test", []string{"YC_TEST_UNSET_VAR_XYZ"}, sandboxOwner{PID: 1})
	if err != nil {
		t.Fatalf("createSessionSecrets: %v", err)
	}
	if len(mounts) != 0 {
		t.Fatalf("mounts = %v, want empty for an unset env var", mounts)
	}
}

func TestCreateSessionSecrets_PreservesEmptyValue(t *testing.T) {
	t.Setenv("YC_TEST_EMPTY_VAR_XYZ", "")

	origRM := podmanSecretRM
	origCreate := podmanSecretCreate
	defer func() {
		podmanSecretRM = origRM
		podmanSecretCreate = origCreate
	}()
	podmanSecretRM = func(context.Context, string) error { return nil }
	var gotValue string
	podmanSecretCreate = func(_ context.Context, _ string, value string, _ []string) error {
		gotValue = value
		return nil
	}

	mounts, err := createSessionSecrets(context.Background(), "yc-test", []string{"YC_TEST_EMPTY_VAR_XYZ"}, sandboxOwner{PID: 1})
	if err != nil {
		t.Fatalf("createSessionSecrets: %v", err)
	}
	if len(mounts) != 1 || gotValue != "" {
		t.Fatalf("mounts = %+v, created value = %q, want one empty-valued secret", mounts, gotValue)
	}
}

func TestCreateSessionSecrets_CreatesOneSecretPerSetValue(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_faketoken")

	origRM := podmanSecretRM
	origCreate := podmanSecretCreate
	defer func() {
		podmanSecretRM = origRM
		podmanSecretCreate = origCreate
	}()
	var rmCalls []string
	var createdName, createdValue string
	var createdLabels []string
	podmanSecretRM = func(_ context.Context, name string) error {
		rmCalls = append(rmCalls, name)
		return errors.New("no such secret")
	}
	podmanSecretCreate = func(_ context.Context, name, value string, labels []string) error {
		createdName, createdValue, createdLabels = name, value, labels
		return nil
	}

	mounts, err := createSessionSecrets(context.Background(), "yc-session1", []string{"GITHUB_TOKEN"}, sandboxOwner{PID: 4242, StartTicks: 999, HaveStartTicks: true})
	if err != nil {
		t.Fatalf("createSessionSecrets: %v", err)
	}
	if len(mounts) != 1 || mounts[0].EnvName != "GITHUB_TOKEN" {
		t.Fatalf("mounts = %+v, want one GITHUB_TOKEN mount", mounts)
	}
	wantSecretName := "yc-session1-secret-GITHUB_TOKEN"
	if mounts[0].SecretName != wantSecretName {
		t.Errorf("SecretName = %q, want %q", mounts[0].SecretName, wantSecretName)
	}
	if len(rmCalls) != 1 || rmCalls[0] != wantSecretName {
		t.Errorf("expected a best-effort pre-create rm of %q, got %v", wantSecretName, rmCalls)
	}
	if createdName != wantSecretName || createdValue != "ghp_faketoken" {
		t.Errorf("podman secret create got name=%q value=%q, want %q / ghp_faketoken", createdName, createdValue, wantSecretName)
	}
	wantLabels := []string{"yottacode.owner_pid=4242", "yottacode.owner_started=999"}
	if strings.Join(createdLabels, ",") != strings.Join(wantLabels, ",") {
		t.Errorf("labels = %v, want %v", createdLabels, wantLabels)
	}
}

// TestCreateSessionSecrets_FailureRollsBackEarlierSecrets guards a
// multi-name config where a later secret's creation fails: the ones already
// created in this same call must not leak.
func TestCreateSessionSecrets_FailureRollsBackEarlierSecrets(t *testing.T) {
	t.Setenv("FIRST_TOKEN", "one")
	t.Setenv("SECOND_TOKEN", "two")

	origRM := podmanSecretRM
	origCreate := podmanSecretCreate
	defer func() {
		podmanSecretRM = origRM
		podmanSecretCreate = origCreate
	}()
	var removed []string
	podmanSecretRM = func(_ context.Context, name string) error {
		removed = append(removed, name)
		return nil
	}
	podmanSecretCreate = func(_ context.Context, name, value string, labels []string) error {
		if name == "yc-test-secret-SECOND_TOKEN" {
			return errors.New("boom")
		}
		return nil
	}

	_, err := createSessionSecrets(context.Background(), "yc-test", []string{"FIRST_TOKEN", "SECOND_TOKEN"}, sandboxOwner{PID: 1})
	if err == nil {
		t.Fatal("createSessionSecrets = nil error, want the second create's failure surfaced")
	}
	// removed contains both the pre-create sweeps (FIRST, SECOND) and the
	// rollback of FIRST after SECOND's create failed.
	count := 0
	for _, name := range removed {
		if name == "yc-test-secret-FIRST_TOKEN" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected FIRST_TOKEN's secret removed twice (pre-sweep + rollback), got %d in %v", count, removed)
	}
}

func TestRemoveSecrets_BestEffortIgnoresErrors(t *testing.T) {
	origRM := podmanSecretRM
	defer func() { podmanSecretRM = origRM }()
	var got []string
	podmanSecretRM = func(_ context.Context, name string) error {
		got = append(got, name)
		return errors.New("boom")
	}
	removeSecrets(context.Background(), []secretMount{{SecretName: "a"}, {SecretName: "b"}})
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("removeSecrets called rm for %v, want [a b] despite errors", got)
	}
}

func TestSecretExportPrelude_BuildsOneExportPerMount(t *testing.T) {
	got := secretExportPrelude([]secretMount{
		{SecretName: "yc-x-secret-GITHUB_TOKEN", EnvName: "GITHUB_TOKEN"},
		{SecretName: "yc-x-secret-NPM_TOKEN", EnvName: "NPM_TOKEN"},
	})
	want := `export GITHUB_TOKEN="$(cat /run/secrets/GITHUB_TOKEN 2>/dev/null)"
export NPM_TOKEN="$(cat /run/secrets/NPM_TOKEN 2>/dev/null)"
`
	if got != want {
		t.Errorf("secretExportPrelude =\n%q\nwant\n%q", got, want)
	}
}

func TestSecretExportPrelude_EmptyForNoMounts(t *testing.T) {
	if got := secretExportPrelude(nil); got != "" {
		t.Errorf("secretExportPrelude(nil) = %q, want empty", got)
	}
}

func TestSecretMountNames(t *testing.T) {
	got := secretMountNames([]secretMount{{SecretName: "a"}, {SecretName: "b"}})
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("secretMountNames = %v, want [a b]", got)
	}
}
