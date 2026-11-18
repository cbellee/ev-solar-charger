package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelemetryCertificateReload(t *testing.T) {
	for _, tool := range []string{"bash", "openssl", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s required for certificate script test", tool)
		}
	}
	root := t.TempDir()
	write := func(name, contents string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(contents), mode); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile("telemetry-cert.sh")
	if err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(root, "scripts", "telemetry-cert.sh")
	write(scriptPath, string(script), 0600)
	bin := filepath.Join(root, "bin")
	write(filepath.Join(bin, "id"), "#!/bin/sh\necho 0\n", 0700)
	write(filepath.Join(bin, "chown"), "#!/bin/sh\nexit 0\n", 0700)
	write(filepath.Join(bin, "curl"), "#!/bin/sh\nexit 0\n", 0700)
	write(filepath.Join(bin, "docker"), `#!/bin/sh
case " $* " in
  *" version "*) exit 0 ;;
  *" run "*)
    case " $* " in
      *" telemetry-acme --renew-days=30 "|*" telemetry-acme ") exit 0 ;;
      *) echo "invalid Lego v5 invocation" >&2; exit 1 ;;
    esac
    ;;
  *" ps -q fleet-telemetry "*) echo test-receiver ;;
  *" inspect "*) echo true ;;
  *" restart fleet-telemetry "*)
    echo restart >> "$ACTION_LOG"
    if [ "$FAIL_RESTART" = 1 ]; then exit 1; fi
    ;;
  *) echo "unexpected docker invocation" >&2; exit 1 ;;
esac
`, 0700)
	state := filepath.Join(root, "data", "telemetry")
	write(filepath.Join(state, "cloudflare-dns-api-token"), "fake-test-token\n", 0600)
	source := filepath.Join(state, "acme", "certificates")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	issueFixture := func() {
		t.Helper()
		cmd := exec.Command("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
			"-subj", "/CN=telemetry.ev.bellee.net",
			"-keyout", filepath.Join(source, "telemetry.ev.bellee.net.key"),
			"-out", filepath.Join(source, "telemetry.ev.bellee.net.crt"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture certificate: %v: %s", err, out)
		}
	}
	logPath := filepath.Join(root, "actions")
	action := "issue"
	run := func(fail bool) error {
		t.Helper()
		cmd := exec.Command("bash", scriptPath, action)
		failure := "0"
		if fail {
			failure = "1"
		}
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"ACTION_LOG="+logPath, "FAIL_RESTART="+failure)
		out, err := cmd.CombinedOutput()
		if err != nil && !fail {
			t.Fatalf("certificate task: %v: %s", err, out)
		}
		return err
	}
	issueFixture()
	run(false)
	action = "renew"
	marker := filepath.Join(state, "receiver-cert-fingerprint")
	original, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	run(false)
	actions, err := os.ReadFile(logPath)
	if err != nil || strings.Count(string(actions), "restart") != 1 {
		t.Fatalf("unchanged certificate restarted the receiver: %q, %v", actions, err)
	}
	keyInfo, err := os.Stat(filepath.Join(state, "tls", "tls.key"))
	if err != nil || keyInfo.Mode().Perm() != 0640 {
		t.Fatalf("deployed key permissions are not restricted: %v", err)
	}
	issueFixture()
	if err := run(true); err == nil {
		t.Fatal("restart failure was swallowed")
	}
	afterFailure, err := os.ReadFile(marker)
	if err != nil || string(afterFailure) != string(original) {
		t.Fatal("failed reload incorrectly marked the new certificate loaded")
	}
	run(false)
	actions, err = os.ReadFile(logPath)
	if err != nil || strings.Count(string(actions), "restart") != 3 {
		t.Fatalf("next task did not retry the failed reload: %q, %v", actions, err)
	}
}

func TestTelemetryLegoCLI(t *testing.T) {
	if os.Getenv("TELEMETRY_TEST_LEGO_CLI") != "1" {
		t.Skip("set TELEMETRY_TEST_LEGO_CLI=1 to validate the pinned ACME image CLI")
	}
	cmd := exec.Command("docker", "compose", "--env-file", "/dev/null",
		"-f", "../docker-compose.telemetry.yml", "--profile", "telemetry-tools", "config",
		"--no-consistency", "--no-env-resolution", "--format", "json")
	cmd.Env = append(os.Environ(), "TELEMETRY_ACME_EMAIL=test@example.invalid")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("render telemetry Compose configuration: %v", err)
	}
	var compose struct {
		Services map[string]struct {
			Image      string   `json:"image"`
			Entrypoint []string `json:"entrypoint"`
		} `json:"services"`
	}
	if err := json.Unmarshal(output, &compose); err != nil {
		t.Fatal(err)
	}
	service := compose.Services["telemetry-acme"]
	if len(service.Entrypoint) == 0 || service.Image == "" {
		t.Fatal("ACME image or entrypoint missing")
	}
	for _, action := range []string{"issue", "renew"} {
		t.Run(action, func(t *testing.T) {
			args := []string{"run", "--rm", "--network", "none", "--entrypoint", service.Entrypoint[0], service.Image}
			args = append(args, service.Entrypoint[1:]...)
			if action == "renew" {
				args = append(args, "--renew-days=30")
			}
			args = append(args, "--help")
			if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
				t.Fatalf("pinned Lego rejected %s arguments: %v\n%s", action, err, out)
			}
		})
	}
}
