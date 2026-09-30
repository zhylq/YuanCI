package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
)

func serviceTestExecutor(t *testing.T, mode string) (*DockerExecutor, string) {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "calls.log")
	e := NewDockerExecutor(io.Discard, io.Discard)
	e.command = func(ctx context.Context, name string, arguments ...string) *exec.Cmd {
		args := append([]string{"-test.run=^TestDockerServiceHelperProcess$", "--"}, arguments...)
		cmd := exec.CommandContext(ctx, os.Args[0], args...)
		cmd.Env = append(os.Environ(), "YUANCI_SERVICE_HELPER=1", "YUANCI_SERVICE_CALLS="+logFile, "YUANCI_SERVICE_MODE="+mode)
		return cmd
	}
	return e, logFile
}

func serviceTestJob() pipeline.PlanJob {
	return pipeline.PlanJob{Name: "test", Image: "alpine:3.21", Timeout: time.Second * 10,
		Services: []pipeline.Service{{Name: "db", Image: "postgres:17", Environment: map[string]string{"POSTGRES_DB": "test"}}, {Name: "cache", Image: "redis:7"}},
		Steps:    []pipeline.Step{{Name: "query", Commands: []string{"true"}}}}
}

func TestDockerServicesLifecycle(t *testing.T) {
	for _, mode := range []string{"healthy", "no-health", "starting-healthy"} {
		t.Run(mode, func(t *testing.T) {
			e, file := serviceTestExecutor(t, mode)
			id := uuid.New()
			if err := e.Execute(t.Context(), id, serviceTestJob(), nil); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			calls := string(body)
			for _, alias := range []string{"db", "cache"} {
				if !strings.Contains(calls, "--network-alias "+alias) {
					t.Fatalf("missing alias %s: %s", alias, calls)
				}
			}
			step := strings.Index(calls, "run --rm --name")
			ready := strings.LastIndex(calls, "container inspect")
			if ready < 0 || step <= ready {
				t.Fatalf("steps started before readiness: %s", calls)
			}
			serviceCleanup := strings.Index(calls, "container rm -f -v")
			if serviceCleanup < step || strings.Index(calls, "network rm") <= serviceCleanup {
				t.Fatalf("cleanup order: %s", calls)
			}
			for _, forbidden := range []string{"--publish", "--privileged", "--network host", "/var/run/docker.sock"} {
				if strings.Contains(calls, forbidden) {
					t.Fatalf("unsafe service: %s", forbidden)
				}
			}
		})
	}
}

func TestDockerServicesFailureCleansUpBeforeSteps(t *testing.T) {
	for _, mode := range []string{"unhealthy", "exited", "start-fail", "create-fail", "create-second-fail", "bad-state", "oversized-state"} {
		t.Run(mode, func(t *testing.T) {
			e, file := serviceTestExecutor(t, mode)
			if err := e.Execute(t.Context(), uuid.New(), serviceTestJob(), nil); err == nil {
				t.Fatal("failed service accepted")
			}
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			calls := string(body)
			if strings.Contains(calls, "run --rm --name") {
				t.Fatalf("user step ran: %s", calls)
			}
			for _, want := range []string{"container rm -f -v", "network rm", "volume rm -f"} {
				if !strings.Contains(calls, want) {
					t.Fatalf("missing cleanup %s: %s", want, calls)
				}
			}
		})
	}
}

func TestDockerServiceCancellation(t *testing.T) {
	e, file := serviceTestExecutor(t, "starting")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.Execute(ctx, uuid.New(), serviceTestJob(), nil) }()
	waitForDockerCall(t, file, "container inspect", 5*time.Second)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled service succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("service readiness ignored cancellation")
	}
	body, _ := os.ReadFile(file)
	if !strings.Contains(string(body), "container rm -f -v") {
		t.Fatal("canceled service was not cleaned up")
	}
}

func TestDockerServiceReadinessDeadline(t *testing.T) {
	e, file := serviceTestExecutor(t, "starting")
	e.ServiceReadyTimeout = 100 * time.Millisecond
	e.ServicePollInterval = 10 * time.Millisecond
	err := e.Execute(t.Context(), uuid.New(), serviceTestJob(), nil)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("readiness deadline: %v", err)
	}
	body, _ := os.ReadFile(file)
	if strings.Contains(string(body), "run --rm --name") || !strings.Contains(string(body), "container rm -f -v") {
		t.Fatal("readiness timeout ran steps or skipped cleanup")
	}
}

func TestDockerServiceRuntimeValidation(t *testing.T) {
	e, file := serviceTestExecutor(t, "healthy")
	job := serviceTestJob()
	job.Services[0].Image = "--privileged"
	if err := e.Execute(t.Context(), uuid.New(), job, nil); err == nil {
		t.Fatal("unsafe persisted plan accepted")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("Docker invoked for invalid plan")
	}
}

func TestDockerServiceFailureDoesNotExposeEnvironment(t *testing.T) {
	e, _ := serviceTestExecutor(t, "create-fail")
	var diagnostics bytes.Buffer
	e.Stderr = &diagnostics
	job := serviceTestJob()
	job.Services[0].Environment["TEST_VALUE"] = "synthetic-service-environment"
	if err := e.Execute(t.Context(), uuid.New(), job, nil); err == nil {
		t.Fatal("creation failure accepted")
	}
	if strings.Contains(diagnostics.String(), "synthetic-service-environment") || !strings.Contains(diagnostics.String(), "[services] Startup failed") {
		t.Fatal("startup diagnostic exposed service output or omitted failure")
	}
}

func TestDockerServiceStepFailureCleanup(t *testing.T) {
	e, file := serviceTestExecutor(t, "step-fail")
	if err := e.Execute(t.Context(), uuid.New(), serviceTestJob(), nil); err == nil {
		t.Fatal("step failure accepted")
	}
	body, _ := os.ReadFile(file)
	for _, want := range []string{"run --rm --name", "container rm -f -v", "network rm", "volume rm -f"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("step failure omitted %s", want)
		}
	}
}

func TestDockerServicesIntegration(t *testing.T) {
	if os.Getenv("YUANCI_TEST_DOCKER") != "1" {
		t.Skip("set YUANCI_TEST_DOCKER=1 with a dedicated Docker test daemon")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	probeCtx, stopProbe := context.WithTimeout(ctx, 15*time.Second)
	err := NewDockerExecutor(io.Discard, io.Discard).Check(probeCtx)
	stopProbe()
	if err != nil {
		t.Fatalf("Docker test daemon is unavailable: %v", err)
	}
	runDocker := func(args ...string) []byte {
		t.Helper()
		body, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v: %s", args[0], err, body)
		}
		return body
	}
	fixture := t.TempDir()
	image := "yuanci-service-test:" + uuid.NewString()
	file := `FROM alpine:3.21
RUN apk add --no-cache busybox-extras && mkdir /www && echo service-ready >/www/index.html
HEALTHCHECK --interval=1s --timeout=1s --retries=1 CMD wget -q -O - http://127.0.0.1:8080 || exit 1
CMD ["sh", "-ec", "if [ \"$MODE\" = unhealthy ]; then exec sleep 300; else exec busybox-extras httpd -f -p 8080 -h /www; fi"]
`
	if err := os.WriteFile(filepath.Join(fixture, "Dockerfile"), []byte(file), 0600); err != nil {
		t.Fatal(err)
	}
	runDocker("build", "--quiet", "-t", image, fixture)
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanupCtx, "docker", "image", "rm", image).Run()
	})
	for _, mode := range []string{"healthy", "unhealthy", "postgres", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			id := uuid.New()
			e := NewDockerExecutor(io.Discard, io.Discard)
			e.ServiceReadyTimeout = 20 * time.Second
			var anonymousVolumes []string
			e.command = func(cmdCtx context.Context, binary string, arguments ...string) *exec.Cmd {
				if len(arguments) > 1 && arguments[0] == "container" && arguments[1] == "inspect" && len(anonymousVolumes) == 0 {
					body, err := exec.CommandContext(cmdCtx, binary, "container", "inspect", "--format", `{{range .Mounts}}{{if eq .Type "volume"}}{{println .Name}}{{end}}{{end}}`, dockerServiceContainerName(id, 0)).Output()
					if err == nil {
						anonymousVolumes = strings.Fields(string(body))
					}
				}
				return exec.CommandContext(cmdCtx, binary, arguments...)
			}
			job := pipeline.PlanJob{Name: "docker-service", Image: "alpine:3.21", Timeout: 90 * time.Second,
				Services: []pipeline.Service{{Name: "web", Image: image, Environment: map[string]string{"MODE": mode}}},
				Steps:    []pipeline.Step{{Name: "query", Commands: []string{"wget -q -O - http://web:8080 | grep service-ready"}}}}
			if mode == "postgres" {
				job.Image = "postgres:17-alpine"
				job.Services = []pipeline.Service{{Name: "db", Image: job.Image, Environment: map[string]string{"POSTGRES_USER": "test", "POSTGRES_PASSWORD": "disposable-service-test"}}}
				job.Steps[0].Commands = []string{"i=0; until pg_isready -h db -U test; do i=$((i+1)); test $i -lt 30; sleep 1; done", "PGPASSWORD=disposable-service-test psql -h db -U test -d test -c 'select 1'"}
			}
			executeCtx, stop := context.WithCancel(ctx)
			defer stop()
			if mode == "cancel" {
				job.Steps[0].Commands = []string{"sleep 30"}
			}
			done := make(chan error, 1)
			go func() { done <- e.Execute(executeCtx, id, job, nil) }()
			if mode == "cancel" {
				deadline := time.Now().Add(30 * time.Second)
				for {
					body, err := exec.CommandContext(ctx, "docker", "container", "inspect", "--format", "{{.State.Running}}", dockerContainerName(id, 0)).Output()
					if err == nil && strings.TrimSpace(string(body)) == "true" {
						break
					}
					if time.Now().After(deadline) {
						stop()
						<-done
						t.Fatal("step container did not start")
					}
					time.Sleep(100 * time.Millisecond)
				}
				stop()
			}
			err := <-done
			if mode == "unhealthy" || mode == "cancel" {
				if err == nil {
					t.Fatal("failed or canceled execution succeeded")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			volume, network := dockerResourceNames(id)
			targets := [][]string{
				{"container", "inspect", dockerServiceContainerName(id, 0)},
				{"container", "inspect", dockerContainerName(id, 0)},
				{"network", "inspect", network}, {"volume", "inspect", volume},
			}
			for _, name := range anonymousVolumes {
				targets = append(targets, []string{"volume", "inspect", name})
			}
			if mode == "postgres" && len(anonymousVolumes) == 0 {
				t.Fatal("PostgreSQL anonymous data volume was not inspected")
			}
			for _, target := range targets {
				body, err := exec.CommandContext(ctx, "docker", target...).CombinedOutput()
				message := strings.ToLower(string(body))
				if err == nil || !(strings.Contains(message, "no such") || strings.Contains(message, "not found")) {
					t.Fatalf("resource cleanup not verified: %v: %v: %s", target, err, body)
				}
			}
		})
	}
}

func TestDockerServiceHelperProcess(t *testing.T) {
	if os.Getenv("YUANCI_SERVICE_HELPER") != "1" {
		return
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i + 1
			break
		}
	}
	args := os.Args[separator:]
	file := os.Getenv("YUANCI_SERVICE_CALLS")
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(f, strings.Join(args, " "))
	_ = f.Close()
	joined := strings.Join(args, " ")
	mode := os.Getenv("YUANCI_SERVICE_MODE")
	if (mode == "start-fail" && strings.HasPrefix(joined, "container start")) ||
		(mode == "create-fail" && strings.HasPrefix(joined, "create")) ||
		(mode == "create-second-fail" && strings.HasPrefix(joined, "create") && strings.Contains(joined, "-service-1 ")) {
		_, _ = fmt.Fprintln(os.Stderr, "synthetic-service-environment")
		os.Exit(42)
	}
	if mode == "step-fail" && strings.HasPrefix(joined, "run --rm") {
		os.Exit(43)
	}
	if strings.HasPrefix(joined, "container inspect") {
		state := `{"status":"running","running":true,"health":"healthy"}`
		switch mode {
		case "no-health":
			state = `{"status":"running","running":true,"health":""}`
		case "starting":
			state = `{"status":"running","running":true,"health":"starting"}`
		case "starting-healthy":
			body, _ := os.ReadFile(file)
			if strings.Count(string(body), "container inspect") == 1 {
				state = `{"status":"running","running":true,"health":"starting"}`
			}
		case "unhealthy":
			state = `{"status":"running","running":true,"health":"unhealthy"}`
		case "exited":
			state = `{"status":"exited","running":false,"health":""}`
		case "bad-state":
			state = "not-json"
		case "oversized-state":
			state = strings.Repeat("x", 8192)
		}
		_, _ = fmt.Fprintln(os.Stdout, state)
	}
	os.Exit(0)
}
