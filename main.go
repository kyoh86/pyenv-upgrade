package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"

	"github.com/alecthomas/kingpin"
	"github.com/kyoh86/ask"
)

// nolint
var (
	version = "snapshot"
	commit  = "snapshot"
	date    = "snapshot"
)

func main() {
	app := kingpin.New("pyenv-upgrade", "Upgrade all pyenv-envs").Version(version).Author("kyoh86")
	kingpin.MustParse(app.Parse(os.Args[1:]))

	verbose := true
	locals := localVersions(verbose)
	remoteLatests := remoteLatestVersions(verbose)

	localLatests := map[int]semantic{}
	for _, loc := range locals {
		if old, ok := localLatests[loc.version.major]; ok {
			if old.isNewerThan(loc.version) {
				continue
			}
		}
		localLatests[loc.version.major] = loc.version
	}

	for major, local := range localLatests {
		rem, ok := remoteLatests[major]
		if ok && rem.isNewerThan(local) {
			res, err := ask.Message(fmt.Sprintf("Install %s?", rem)).YesNo()
			if err != nil {
				log.Fatal(err)
			}
			if *res {
				if err := installVersion(verbose, rem); err != nil {
					log.Fatal(err)
				}
				localLatests[major] = rem
			}
		}
	}

	for _, loc := range locals {
		if loc.environ == "" {
			continue
		}
		lat, ok := localLatests[loc.version.major]
		if ok && lat.isNewerThan(loc.version) {
			res, err := ask.Message(fmt.Sprintf("Update %s to %s?", loc, lat)).YesNo()
			if err != nil {
				log.Fatal(err)
			}
			if *res {
				if err := updateVersion(verbose, loc, lat); err != nil {
					log.Fatal(err)
				}
			}
		}
	}
}

type semantic struct {
	major int
	minor int
	patch int
}

func (v semantic) String() string {
	if v.patch > 0 {
		return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
	} else if v.minor > 0 {
		return fmt.Sprintf("%d.%d", v.major, v.minor)
	}
	return fmt.Sprintf("%d", v.major)
}

func (v semantic) isNewerThan(another semantic) bool {
	switch {
	case v.major < another.major:
		return false
	case v.major > another.major:
		return true
	}
	switch {
	case v.minor < another.minor:
		return false
	case v.minor > another.minor:
		return true
	}
	return v.patch > another.patch
}

type local struct {
	current bool
	environ string
	version semantic
}

func (v local) String() string {
	return fmt.Sprintf("%s/envs/%s", v.version, v.environ)
}

func installVersion(verbose bool, ver semantic) error {
	log.Printf("Install a version %s", ver)
	if _, err := pipe(verbose, "pyenv", "install", ver.String()); err != nil {
		return err
	}
	_, err := pipeInVer(ver.String(), verbose, "pip", "install", "--upgrade", "pip")
	return err
}

func updateVersion(verbose bool, loc local, ver semantic) error {
	log.Printf("Freezing pip in %s", loc)
	res, err := pipeInVer(loc.environ, verbose, "pip", "freeze")
	if err != nil {
		return err
	}
	tmp, err := putTempFile(res)
	if err != nil {
		return err
	}

	log.Printf("Uninstalling %s", loc)
	if _, err := pipeInVer("system", verbose, "pyenv", "uninstall", "-f", loc.environ); err != nil {
		return err
	}

	newEnv := local{environ: loc.environ, version: ver}
	log.Printf("Creating %s", newEnv)
	if _, err := pipeInVer("system", verbose, "pyenv", "virtualenv", ver.String(), loc.environ); err != nil {
		return err
	}

	log.Printf("Unfreezing %s", newEnv)
	_, err = pipeInVer(newEnv.environ, verbose, "pip", "install", "-r", tmp)
	return err
}

func putTempFile(body io.Reader) (name string, retErr error) {
	tmp, err := ioutil.TempFile(os.TempDir(), "pyenv")
	if err != nil {
		return "", err
	}
	name = tmp.Name()
	defer func() {
		if err := tmp.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()

	_, retErr = io.Copy(tmp, body)
	return
}

func remoteLatestVersions(verbose bool) map[int]semantic {
	log.Print("Get installable versions...")
	res, err := pipe(verbose, "pyenv", "install", "--list")
	if err != nil {
		log.Fatal(err)
	}
	vers := map[int]semantic{}
	verReg := regexp.MustCompile(`^\s*(\d+)(?:\.(\d+))?(?:\.(\d+))?$`)
	scanner := bufio.NewScanner(res)
	for scanner.Scan() {
		line := scanner.Text()
		mat := verReg.FindStringSubmatch(line)
		if len(mat) <= 3 {
			continue
		}
		ver := semantic{}
		ver.major, _ = strconv.Atoi(mat[1])
		ver.minor, _ = strconv.Atoi(mat[2])
		ver.patch, _ = strconv.Atoi(mat[3])
		if old, ok := vers[ver.major]; ok {
			if old.isNewerThan(ver) {
				continue
			}
		}
		vers[ver.major] = ver
	}
	return vers
}

func localVersions(verbose bool) []local {
	log.Print("Get local versions...")
	res, err := pipe(verbose, "pyenv", "versions")
	if err != nil {
		log.Fatal(err)
	}

	var locals []local
	verReg := regexp.MustCompile(`^([\* ]) (\d+)(?:\.(\d+))?(?:\.(\d+))?(?:/envs/([^ ]+))?(?: \(set by .+\))?$`)
	scanner := bufio.NewScanner(res)
	envs := map[string]struct{}{}
	for scanner.Scan() {
		line := scanner.Text()
		mat := verReg.FindStringSubmatch(line)
		loc := local{}
		if len(mat) > 4 {
			loc.current = mat[1] == "*"
			loc.version.major, _ = strconv.Atoi(mat[2])
			loc.version.minor, _ = strconv.Atoi(mat[3])
			loc.version.patch, _ = strconv.Atoi(mat[4])
			if len(mat) > 5 {
				loc.environ = mat[5]
				envs[mat[5]] = struct{}{}
			}
			if _, ok := envs[mat[2]+mat[3]]; ok {
				continue
			}
			locals = append(locals, loc)
		}
	}
	return locals
}

func pipeInVer(version string, verbose bool, exe string, args ...string) (io.Reader, error) {
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("PYENV_VERSION=%s", version))
	return pipeCmd(verbose, cmd)
}

func pipe(verbose bool, exe string, args ...string) (io.Reader, error) {
	cmd := exec.Command(exe, args...)
	cmd.Env = os.Environ()
	return pipeCmd(verbose, cmd)
}

func pipeCmd(verbose bool, cmd *exec.Cmd) (io.Reader, error) {
	buf := &bytes.Buffer{}
	cmd.Stdout = buf
	if verbose {
		cmd.Stdout = io.MultiWriter(os.Stdout, buf)
	}
	cmd.Stderr = os.Stderr
	return buf, cmd.Run()
}
