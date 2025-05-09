package podman

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
)

type StepConnectPodman struct{}

func (s *StepConnectPodman) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	config, ok := state.Get("config").(*Config)
	if !ok {
		err := fmt.Errorf("error encountered obtaining podman config") //nolint:staticcheck
		state.Put("error", err)
		return multistep.ActionHalt
	}

	containerId := state.Get("container_id").(string)
	driver := state.Get("driver").(Driver)
	tempDir := state.Get("temp_dir").(string)

	// Get the version so we can pass it to the communicator
	version, err := driver.Version()
	if err != nil {
		state.Put("error", err)
		return multistep.ActionHalt
	}

	containerUser, err := getContainerUser(containerId)
	if err != nil {
		state.Put("error", err)
		return multistep.ActionHalt
	}

	// Create the communicator that talks to Podman via various
	// os/exec tricks.
	comm := &Communicator{
		ContainerID:   containerId,
		HostDir:       tempDir,
		ContainerDir:  config.ContainerDir,
		Version:       version,
		Config:        config,
		ContainerUser: containerUser,
		EntryPoint:    []string{"/bin/sh", "-c"},
	}
	state.Put("communicator", comm)
	return multistep.ActionContinue
}

func (s *StepConnectPodman) Cleanup(state multistep.StateBag) {}

func getContainerUser(containerId string) (string, error) {
	inspectArgs := []string{"podman", "inspect", "--format", "{{.Config.User}}", containerId}
	stdout, err := exec.Command(inspectArgs[0], inspectArgs[1:]...).Output()
	if err != nil {
		errStr := fmt.Sprintf("Failed to inspect the container: %s", err)
		if ee, ok := err.(*exec.ExitError); ok {
			errStr = fmt.Sprintf("%s, %s", errStr, ee.Stderr)
		}
		return "", fmt.Errorf(errStr) //nolint:staticcheck
	}
	return strings.TrimSpace(string(stdout)), nil
}
