package podman

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
)

func testStepExportState(t *testing.T) multistep.StateBag {
	state := testState(t)
	state.Put("container_id", "foo")
	return state
}

func TestStepExport_impl(t *testing.T) {
	var _ multistep.Step = new(StepExport)
}

func TestStepExport(t *testing.T) {
	state := testStepExportState(t)
	step := new(StepExport)
	defer step.Cleanup(state)

	// Create a tempfile for our output path
	tf, err := os.CreateTemp("", "packer")
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	tf.Close()                 //nolint:errcheck
	defer os.Remove(tf.Name()) //nolint:errcheck

	config := state.Get("config").(*Config)
	config.ExportPath = tf.Name()
	driver := state.Get("driver").(*MockDriver)
	driver.ExportReader = bytes.NewReader([]byte("data!"))

	// run the step
	if action := step.Run(context.Background(), state); action != multistep.ActionContinue {
		t.Fatalf("bad action: %#v", action)
	}

	// verify we did the right thing
	if !driver.ExportCalled {
		t.Fatal("should've exported")
	}
	if driver.ExportID != "foo" {
		t.Fatalf("bad: %#v", driver.ExportID)
	}

	// verify the data exported to the file
	contents, err := os.ReadFile(tf.Name())
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	if string(contents) != "data!" {
		t.Fatalf("bad: %#v", string(contents))
	}
}

func TestStepExport_error(t *testing.T) {
	state := testStepExportState(t)
	step := new(StepExport)
	defer step.Cleanup(state)

	// Create a tempfile for our output path
	tf, err := os.CreateTemp("", "packer")
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	tf.Close() //nolint:errcheck

	if err := os.Remove(tf.Name()); err != nil {
		t.Fatalf("err: %s", err)
	}

	config := state.Get("config").(*Config)
	config.ExportPath = tf.Name()
	driver := state.Get("driver").(*MockDriver)
	driver.ExportError = errors.New("foo")

	// run the step
	if action := step.Run(context.Background(), state); action != multistep.ActionHalt {
		t.Fatalf("bad action: %#v", action)
	}

	// verify we have an error
	if _, ok := state.GetOk("error"); !ok {
		t.Fatal("should have error")
	}

	// verify we didn't make that file
	if _, err := os.Stat(tf.Name()); err == nil {
		t.Fatal("export path shouldn't exist")
	}
}
