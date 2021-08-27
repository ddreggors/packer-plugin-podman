package podman

import (
	"io"

	"github.com/hashicorp/go-version"
)

// Driver is the interface that has to be implemented to communicate with
// Podman. The Driver interface also allows the steps to be tested since
// a mock driver can be shimmed in.
type Driver interface {
	// Commit the container to a tag
	Commit(id string, author string, changes []string, message string) (string, error)

	// Delete an image that is imported into Podman
	DeleteImage(id string) error

	// Export exports the container with the given ID to the given writer.
	Export(id string, dst io.Writer) error

	// Import imports a container from a tar file
	Import(path string, changes []string, repo string) (string, error)

	// IPAddress returns the address of the container that can be used
	// for external access.
	IPAddress(id string) (string, error)

	// Sha256 returns the sha256 id of the image
	Sha256(id string) (string, error)

	// Login. This will lock the driver from performing another Login
	// until Logout is called. Therefore, any users MUST call Logout.
	Login(repo, username, password string) error

	// Logout. This can only be called if Login succeeded.
	Logout(repo string) error

	// Pull should pull down the given image.
	Pull(image string) error

	// Push pushes an image to a Podman index/registry.
	Push(name string) error

	// Save an image with the given ID to the given writer.
	SaveImage(id string, dst io.Writer) error

	// StartContainer starts a container and returns the ID for that container,
	// along with a potential error.
	StartContainer(*ContainerConfig) (string, error)

	// KillContainer forcibly stops a container.
	KillContainer(id string) error

	// StopContainer gently stops a container.
	StopContainer(id string) error

	// TagImage tags the image with the given ID
	TagImage(id string, repo string, force bool) error

	// Verify verifies that the driver can run
	Verify() error

	// Version reads the Podman version
	Version() (*version.Version, error)
}

// ContainerConfig is the configuration used to start a container.
type ContainerConfig struct {
	Image      string
	RunCommand []string
	Device     []string
	CapAdd     []string
	CapDrop    []string
	Volumes    map[string]string
	TmpFs      []string
	Privileged bool
	Systemd string
}

// This is the template that is used for the RunCommand in the ContainerConfig.
type startContainerTemplate struct {
	Image string
}
