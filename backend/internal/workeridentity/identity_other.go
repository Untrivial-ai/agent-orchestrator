//go:build !windows

package workeridentity

import "errors"

type InstallOptions struct {
	DataDir, AccountName, WorkspaceRoot, SessionProfileRoot, LauncherPath string
	PTYHostPath, ServiceName                                              string
	Executables                                                           map[string]string
	GitMetadataRoots                                                      []string
}

func Install(InstallOptions) (Config, error) {
	return Config{}, errors.New("worker identity: Windows only")
}
func LoadPassword(Config) ([]byte, error)      { return nil, errors.New("worker identity: Windows only") }
func SecureContainerRoot(string, string) error { return errors.New("worker identity: Windows only") }
func SecureRuntimeFile(string, string) error   { return errors.New("worker identity: Windows only") }
func SecureHostTree(string) error              { return errors.New("worker identity: Windows only") }
func SecureSessionTreeForLogon(string, string, bool) error {
	return errors.New("worker identity: Windows only")
}
