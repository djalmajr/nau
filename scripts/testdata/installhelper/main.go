// The installer fixture supplies go install and the backend without network or services.
package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	if strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") != "go" {
		if len(os.Args) > 1 && os.Args[1] == "capabilities" {
			os.Stdout.WriteString(`{"schema":1,"ephemeral_job":1,"job_events":1}` + "\n")
		}
		return
	}
	if os.Getenv("INSTALL_FIXTURE_FAIL") == "1" {
		os.Exit(1)
	}
	if len(os.Args) != 3 || os.Args[1] != "install" {
		os.Exit(2)
	}
	src, err := os.Open(os.Getenv("INSTALL_FIXTURE_NAU"))
	if err != nil {
		os.Exit(3)
	}
	defer src.Close()
	name := "nau"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst, err := os.OpenFile(filepath.Join(os.Getenv("GOBIN"), name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0700)
	if err != nil {
		os.Exit(4)
	}
	_, err = io.Copy(dst, src)
	closeErr := dst.Close()
	if err != nil || closeErr != nil {
		os.Exit(5)
	}
}
