package podman

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/hashicorp/go-version"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

type Communicator struct {
	ContainerID   string
	HostDir       string
	ContainerDir  string
	Version       *version.Version
	Config        *Config
	ContainerUser string
	lock          sync.Mutex
	EntryPoint    []string
}

var _ packersdk.Communicator = new(Communicator)

func (c *Communicator) Start(ctx context.Context, remote *packersdk.RemoteCmd) error {
	podmanArgs := []string{
		"exec",
		"-i",
		c.ContainerID,
	}
	podmanArgs = append(podmanArgs, c.EntryPoint...)
	podmanArgs = append(podmanArgs, fmt.Sprintf("(%s)", remote.Command))

	if c.Config.Pty {
		podmanArgs = append(podmanArgs[:2], append([]string{"-t"}, podmanArgs[2:]...)...)
	}

	if c.Config.ExecUser != "" {
		podmanArgs = append(podmanArgs[:2],
			append([]string{"-u", c.Config.ExecUser}, podmanArgs[2:]...)...)
	}

	cmd := exec.Command("podman", podmanArgs...)

	var (
		stdin_w io.WriteCloser
		err     error
	)

	stdin_w, err = cmd.StdinPipe()
	if err != nil {
		return err
	}

	stderr_r, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	stdout_r, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	// Run the actual command in a goroutine so that Start doesn't block
	go c.run(cmd, remote, stdin_w, stdout_r, stderr_r)

	return nil
}

// Upload uploads a file to the podman container
func (c *Communicator) Upload(dst string, src io.Reader, fi *os.FileInfo) error {
	if fi == nil {
		return c.uploadReader(dst, src)
	}
	return c.uploadFile(dst, src, fi)
}

// uploadReader writes an io.Reader to a temporary file before uploading
func (c *Communicator) uploadReader(dst string, src io.Reader) error {
	// Create a temporary file to store the upload
	tempfile, err := os.CreateTemp(c.HostDir, "upload")
	if err != nil {
		return fmt.Errorf("Failed to open temp file for writing: %s", err) //nolint:staticcheck
	}
	defer func() {
		err := os.Remove(tempfile.Name())
		if err != nil {
			// Handle the error appropriately, e.g., log it or return it
			fmt.Fprintf(os.Stderr, "Error removing file: %v\n", err)
		}
	}()
	defer func() {
		err := tempfile.Close()
		if err != nil {
			// Handle the error appropriately, e.g., log it or return it
			fmt.Fprintf(os.Stderr, "Error closing file: %v\n", err)
		}
	}()

	if _, err := io.Copy(tempfile, src); err != nil {
		return fmt.Errorf("Failed to copy upload file to tempfile: %s", err) //nolint:staticcheck
	}
	if _, err := tempfile.Seek(0, 0); err != nil {
		return fmt.Errorf("Error seeking tempfile info: %s", err) //nolint:staticcheck
	}

	fi, err := tempfile.Stat()
	if err != nil {
		return fmt.Errorf("Error getting tempfile info: %s", err) //nolint:staticcheck
	}
	return c.uploadFile(dst, tempfile, &fi)
}

// uploadFile uses podman cp to copy the file from the host to the container
func (c *Communicator) uploadFile(dst string, src io.Reader, fi *os.FileInfo) error {
	// command format: podman cp /path/to/infile containerid:/path/to/outfile
	log.Printf("Copying to %s on container %s.", dst, c.ContainerID)

	localCmd := exec.Command("podman", "cp", "-",
		fmt.Sprintf("%s:%s", c.ContainerID, filepath.Dir(dst)))

	stderrP, err := localCmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("Failed to open pipe: %s", err) //nolint:staticcheck
	}

	stdin, err := localCmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("Failed to open pipe: %s", err) //nolint:staticcheck
	}

	if err := localCmd.Start(); err != nil {
		return err
	}

	archive := tar.NewWriter(stdin)
	header, err := tar.FileInfoHeader(*fi, "")
	if err != nil {
		return err
	}
	header.Name = filepath.Base(dst)
	if err := archive.WriteHeader(header); err != nil {
		return fmt.Errorf("Failed to write header: %s", err) //nolint:staticcheck
	}

	numBytes, err := io.Copy(archive, src)
	if err != nil {
		return fmt.Errorf("Failed to pipe upload: %s", err) //nolint:staticcheck
	}
	log.Printf("Copied %d bytes for %s", numBytes, dst)

	if err := archive.Close(); err != nil {
		return fmt.Errorf("Failed to close archive: %s", err) //nolint:staticcheck
	}
	if err := stdin.Close(); err != nil {
		return fmt.Errorf("Failed to close stdin: %s", err) //nolint:staticcheck
	}

	stderrOut, err := io.ReadAll(stderrP)
	if err != nil {
		return err
	}

	if err := localCmd.Wait(); err != nil {
		return fmt.Errorf("Failed to upload to '%s' in container: %s. %s.", dst, stderrOut, err) //nolint:staticcheck
	}

	if err := c.fixDestinationOwner(dst); err != nil {
		return err
	}

	return nil
}

func (c *Communicator) UploadDir(dst string, src string, exclude []string) error {
	/*
		from https://docs.podman.com/engine/reference/commandline/cp/#extended-description
		SRC_PATH specifies a directory
			DEST_PATH does not exist
				DEST_PATH is created as a directory and the contents of the source directory are copied into this directory
			DEST_PATH exists and is a file
				Error condition: cannot copy a directory to a file
			DEST_PATH exists and is a directory
				SRC_PATH does not end with /. (that is: slash followed by dot)
					the source directory is copied into this directory
				SRC_PATH does end with /. (that is: slash followed by dot)
					the content of the source directory is copied into this directory

		translating that in to our semantics:

		if source ends in /
			podman cp src. dest
		otherwise, cp source dest

	*/

	podmanSource := src
	if src[len(src)-1] == '/' {
		podmanSource = fmt.Sprintf("%s.", src)
	}

	// Make the directory, then copy into it
	localCmd := exec.Command("podman", "cp", podmanSource, fmt.Sprintf("%s:%s", c.ContainerID, dst))

	stderrP, err := localCmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("Failed to open pipe: %s", err) //nolint:staticcheck
	}
	if err := localCmd.Start(); err != nil {
		return fmt.Errorf("Failed to copy: %s", err) //nolint:staticcheck
	}
	stderrOut, err := io.ReadAll(stderrP)
	if err != nil {
		return err
	}

	// Wait for the copy to complete
	if err := localCmd.Wait(); err != nil {
		return fmt.Errorf("Failed to upload to '%s' in container: %s. %s.", dst, stderrOut, err) //nolint:staticcheck
	}

	if err := c.fixDestinationOwner(dst); err != nil {
		return err
	}

	return nil
}

// Download pulls a file out of a container using `podman cp`. We have a source
// path and want to write to an io.Writer, not a file. We use - to make podman
// cp to write to stdout, and then copy the stream to our destination io.Writer.
func (c *Communicator) Download(src string, dst io.Writer) error {
	log.Printf("Downloading file from container: %s:%s", c.ContainerID, src)
	localCmd := exec.Command("podman", "cp", fmt.Sprintf("%s:%s", c.ContainerID, src), "-")

	pipe, err := localCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("Failed to open pipe: %s", err) //nolint:staticcheck
	}

	stderrP, err := localCmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("Failed to open stderr pipe: %s", err) //nolint:staticcheck
	}

	if err = localCmd.Start(); err != nil {
		return fmt.Errorf("Failed to start download: %s", err) //nolint:staticcheck
	}

	// When you use - to send podman cp to stdout it is streamed as a tar; this
	// enables it to work with directories. We don't actually support
	// directories in Download() but we still need to handle the tar format.

	archive := tar.NewReader(pipe)
	_, err = archive.Next()
	if err != nil {
		// see if we can get a useful error message from stderr, since stdout
		// is messed up.
		if stderrOut, err := io.ReadAll(stderrP); err == nil {
			if string(stderrOut) != "" {
				return fmt.Errorf("Error downloading file: %s", string(stderrOut)) //nolint:staticcheck
			}
		}
		return fmt.Errorf("Failed to read header from tar stream: %s", err) //nolint:staticcheck
	}

	numBytes, err := io.Copy(dst, archive)
	if err != nil {
		return fmt.Errorf("Failed to pipe download: %s", err) //nolint:staticcheck
	}
	log.Printf("Copied %d bytes for %s", numBytes, src)

	if err = localCmd.Wait(); err != nil {
		return fmt.Errorf("Failed to download '%s' from container: %s", src, err) //nolint:staticcheck
	}

	return nil
}

func (c *Communicator) DownloadDir(src string, dst string, exclude []string) error {
	return fmt.Errorf("DownloadDir is not implemented for podman") //nolint:staticcheck
}

// Runs the given command and blocks until completion
func (c *Communicator) run(cmd *exec.Cmd, remote *packersdk.RemoteCmd, stdin io.WriteCloser, stdout, stderr io.ReadCloser) {
	// For Podman, remote communication must be serialized since it
	// only supports single execution.
	c.lock.Lock()
	defer c.lock.Unlock()

	wg := sync.WaitGroup{}
	//nolint:errcheck
	repeat := func(w io.Writer, r io.ReadCloser) {
		io.Copy(w, r)
		r.Close()
		wg.Done()
	}

	if remote.Stdout != nil {
		wg.Add(1)
		go repeat(remote.Stdout, stdout)
	}

	if remote.Stderr != nil {
		wg.Add(1)
		go repeat(remote.Stderr, stderr)
	}

	// Start the command
	log.Printf("Executing %s:", strings.Join(cmd.Args, " "))
	if err := cmd.Start(); err != nil {
		log.Printf("Error executing: %s", err)
		remote.SetExited(254)
		return
	}

	var exitStatus int

	if remote.Stdin != nil {
		//nolint:errcheck
		go func() {
			io.Copy(stdin, remote.Stdin)
			// close stdin to support commands that wait for stdin to be closed before exiting.
			stdin.Close()
		}()
	}

	wg.Wait()
	err := cmd.Wait()

	if exitErr, ok := err.(*exec.ExitError); ok {
		exitStatus = 1

		// There is no process-independent way to get the REAL
		// exit status so we just try to go deeper.
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			exitStatus = status.ExitStatus()
		}
	}

	// Set the exit status which triggers waiters
	remote.SetExited(exitStatus)
}

// TODO Workaround for #5307. Remove once #5409 is fixed.
func (c *Communicator) fixDestinationOwner(destination string) error {
	if !c.Config.FixUploadOwner {
		return nil
	}

	owner := c.ContainerUser
	if owner == "" {
		owner = "root"
	}

	chownArgs := []string{
		"podman", "exec", "--user", "root", c.ContainerID, "/bin/sh", "-c",
		fmt.Sprintf("chown -R %s %s", owner, destination),
	}
	if output, err := exec.Command(chownArgs[0], chownArgs[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("Failed to set owner of the uploaded file: %s, %s", err, output) //nolint:staticcheck
	}

	return nil
}
