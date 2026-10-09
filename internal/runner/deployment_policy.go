package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/yuanci/yuanci/internal/pipeline"
)

// DeploymentPolicy is loaded once from administrator-owned configuration. It
// cannot be supplied or extended by a repository execution plan.
type DeploymentPolicy struct {
	repositories map[string]bool
	mounts       []DeploymentMount
}

type DeploymentMount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

type deploymentPolicyFile struct {
	Repositories []struct {
		Provider   string `json:"provider"`
		ExternalID string `json:"external_id"`
	} `json:"repositories"`
	HostMounts []DeploymentMount `json:"host_mounts,omitempty"`
}

var deploymentRepositoryID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

func LoadDeploymentPolicy(filename string) (*DeploymentPolicy, error) {
	if !filepath.IsAbs(filename) {
		return nil, errors.New("deployment policy path must be absolute")
	}
	filename = filepath.Clean(filename)
	workspace, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	// Native Runner development may start inside a subdirectory of the checkout.
	workingDirectory := workspace
	workspace = ""
	for directory := workingDirectory; ; directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, ".git")); err == nil {
			workspace = directory
			break
		}
		if filepath.Dir(directory) == directory {
			break
		}
	}
	resolved, err := filepath.EvalSymlinks(filename)
	if err != nil {
		return nil, errors.New("cannot resolve deployment policy")
	}
	if resolved == "/workspace" || strings.HasPrefix(filepath.ToSlash(resolved), "/workspace/") {
		return nil, errors.New("deployment policy must be outside the workspace")
	}
	if workspace != "" {
		relative, err := filepath.Rel(workspace, resolved)
		if (err != nil && strings.EqualFold(filepath.VolumeName(workspace), filepath.VolumeName(resolved))) || (err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return nil, errors.New("deployment policy must be outside the workspace")
		}
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > 64<<10 ||
		(runtime.GOOS != "windows" && info.Mode().Perm()&0222 != 0) {
		return nil, errors.New("deployment policy must be a bounded regular read-only administrator file")
	}
	if runtime.GOOS != "windows" {
		parent, err := os.Stat(filepath.Dir(resolved))
		if err != nil || parent.Mode().Perm()&0022 != 0 {
			return nil, errors.New("deployment policy directory must not be group or world writable")
		}
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("deployment policy changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		return nil, errors.New("deployment policy exceeds size limit")
	}
	var document deploymentPolicyFile
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("invalid deployment policy JSON")
	}
	if len(document.Repositories) < 1 || len(document.Repositories) > 127 || len(document.HostMounts) > 16 {
		return nil, errors.New("deployment policy repository or mount count exceeds limits")
	}
	policy := &DeploymentPolicy{repositories: make(map[string]bool), mounts: document.HostMounts}
	for _, repository := range document.Repositories {
		if (repository.Provider != "github" && repository.Provider != "gitee") || !deploymentRepositoryID.MatchString(repository.ExternalID) {
			return nil, errors.New("invalid deployment repository identity")
		}
		label := pipeline.DeploymentRepositoryLabel(repository.Provider, repository.ExternalID)
		if policy.repositories[label] {
			return nil, errors.New("duplicate deployment repository")
		}
		policy.repositories[label] = true
	}
	targets := make(map[string]bool)
	for _, mount := range policy.mounts {
		if !filepath.IsAbs(mount.Source) || strings.ContainsAny(mount.Source, ",\r\n\x00") || !validDeploymentTarget(mount.Target) || targets[mount.Target] {
			return nil, errors.New("invalid deployment host mount")
		}
		if _, err := os.Stat(mount.Source); err != nil {
			return nil, errors.New("deployment mount source is unavailable")
		}
		targets[mount.Target] = true
	}
	return policy, nil
}

func validDeploymentTarget(target string) bool {
	if !strings.HasPrefix(target, "/") || target == "/" || path.Clean(target) != target || strings.ContainsAny(target, ",:\\\r\n\x00") {
		return false
	}
	for _, protected := range []string{"/proc", "/sys", "/dev", "/workspace", "/tmp"} {
		if target == protected || strings.HasPrefix(target, protected+"/") || strings.HasPrefix(protected, target+"/") {
			return false
		}
	}
	return true
}

func (policy *DeploymentPolicy) Labels() map[string]string {
	labels := make(map[string]string)
	if policy != nil {
		for label := range policy.repositories {
			labels[label] = "true"
		}
	}
	return labels
}

func (policy *DeploymentPolicy) authorize(source *localSource) error {
	if policy == nil || source == nil || !policy.repositories[pipeline.DeploymentRepositoryLabel(source.provider, source.repositoryID)] {
		return errors.New("deployment repository is not approved by Runner administrator")
	}
	for _, mount := range policy.mounts {
		if _, err := os.Stat(mount.Source); err != nil {
			return errors.New("deployment mount source is unavailable")
		}
	}
	return nil
}

func (policy *DeploymentPolicy) dockerMountArgs() []string {
	var args []string
	for _, mount := range policy.mounts {
		value := "type=bind,src=" + mount.Source + ",dst=" + mount.Target
		if mount.ReadOnly {
			value += ",readonly"
		}
		args = append(args, "--mount", value)
	}
	return args
}
