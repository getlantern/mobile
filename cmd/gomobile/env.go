package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kardianos/osext"
)

// General mobile build environment. Initialized by envInit.
var (
	cwd          string
	gomobilepath string // $GOPATH/pkg/gomobile

	androidEnv map[string][]string // android arch -> []string

	darwinArmEnv   []string
	darwinArm64Env []string
	darwin386Env   []string
	darwinAmd64Env []string

	androidArmNM string
	darwinArmNM  string
)

func buildEnvInit() (cleanup func(), err error) {
	// Find gomobilepath.
	execpath, err := osext.Executable()
	if err != nil {
		return nil, errors.New("Unable to determine directory of gomobile command: " + err.Error())
	}
	gomobilepath = strings.Replace(execpath, fmt.Sprintf("bin%cgomobile", filepath.Separator), fmt.Sprintf("pkg%cgomobile", filepath.Separator), -1)

	if err := envInit(); err != nil {
		return nil, err
	}

	if buildX {
		fmt.Fprintln(xout, "GOMOBILE="+gomobilepath)
	}

	// Check the toolchain is in a good state.
	// Pick a temporary directory for assembling an apk/app.
	if gomobilepath == "" {
		return nil, errors.New("toolchain not installed, run `gomobile init`")
	}
	cleanupFn := func() {
		if buildWork {
			fmt.Printf("WORK=%s\n", tmpdir)
			return
		}
		removeAll(tmpdir)
	}
	if buildN {
		tmpdir = "$WORK"
		cleanupFn = func() {}
	} else {
		verpath := filepath.Join(gomobilepath, "version")
		installedVersion, err := ioutil.ReadFile(verpath)
		if err != nil {
			return nil, errors.New("toolchain partially installed, run `gomobile init` " + err.Error())
		}
		if !bytes.Equal(installedVersion, goVersionOut) {
			return nil, errors.New("toolchain out of date, run `gomobile init`")
		}

		tmpdir, err = ioutil.TempDir("", "gomobile-work-")
		if err != nil {
			return nil, err
		}
	}
	if buildX {
		fmt.Fprintln(xout, "WORK="+tmpdir)
	}

	return cleanupFn, nil
}

func envInit() (err error) {
	// TODO(crawshaw): cwd only used by ctx.Import, which can take "."
	cwd, err = os.Getwd()
	if err != nil {
		return err
	}

	// Setup the cross-compiler environments.

	androidEnv = make(map[string][]string)
	for arch, toolchain := range ndk {
		if goVersion < toolchain.minGoVer {
			continue
		}

		androidEnv[arch] = []string{
			"GOOS=android",
			"GOARCH=" + arch,
			"CC=" + toolchain.Path("gcc"),
			"CXX=" + toolchain.Path("g++"),
			"CGO_ENABLED=1",
		}
		if arch == "arm" {
			androidEnv[arch] = append(androidEnv[arch], "GOARM=7")
		}
	}

	if runtime.GOOS != "darwin" {
		return nil
	}

	clang, cflags, err := envClang("iphoneos")
	if err != nil {
		return err
	}
	darwinArmEnv = []string{
		"GOOS=darwin",
		"GOARCH=arm",
		"GOARM=7",
		"CC=" + clang,
		"CXX=" + clang,
		"CGO_CFLAGS=" + cflags + " -arch " + archClang("arm"),
		"CGO_LDFLAGS=" + cflags + " -arch " + archClang("arm"),
		"CGO_ENABLED=1",
	}
	darwinArmNM = "nm"
	darwinArm64Env = []string{
		"GOOS=darwin",
		"GOARCH=arm64",
		"CC=" + clang,
		"CXX=" + clang,
		"CGO_CFLAGS=" + cflags + " -arch " + archClang("arm64"),
		"CGO_LDFLAGS=" + cflags + " -arch " + archClang("arm64"),
		"CGO_ENABLED=1",
	}

	clang, cflags, err = envClang("iphonesimulator")
	if err != nil {
		return err
	}
	darwin386Env = []string{
		"GOOS=darwin",
		"GOARCH=386",
		"CC=" + clang,
		"CXX=" + clang,
		"CGO_CFLAGS=" + cflags + " -mios-simulator-version-min=6.1 -arch " + archClang("386"),
		"CGO_LDFLAGS=" + cflags + " -mios-simulator-version-min=6.1 -arch " + archClang("386"),
		"CGO_ENABLED=1",
	}
	darwinAmd64Env = []string{
		"GOOS=darwin",
		"GOARCH=amd64",
		"CC=" + clang,
		"CXX=" + clang,
		"CGO_CFLAGS=" + cflags + " -mios-simulator-version-min=6.1 -arch x86_64",
		"CGO_LDFLAGS=" + cflags + " -mios-simulator-version-min=6.1 -arch x86_64",
		"CGO_ENABLED=1",
	}

	return nil
}

func envClang(sdkName string) (clang, cflags string, err error) {
	if buildN {
		return "clang-" + sdkName, "-isysroot=" + sdkName, nil
	}
	cmd := exec.Command("xcrun", "--sdk", sdkName, "--find", "clang")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("xcrun --find: %v\n%s", err, out)
	}
	clang = strings.TrimSpace(string(out))

	cmd = exec.Command("xcrun", "--sdk", sdkName, "--show-sdk-path")
	out, err = cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("xcrun --show-sdk-path: %v\n%s", err, out)
	}
	sdk := strings.TrimSpace(string(out))

	return clang, "-isysroot " + sdk, nil
}

func archClang(goarch string) string {
	switch goarch {
	case "arm":
		return "armv7"
	case "arm64":
		return "arm64"
	case "386":
		return "i386"
	case "amd64":
		return "x86_64"
	default:
		panic(fmt.Sprintf("unknown GOARCH: %q", goarch))
	}
}

// environ merges os.Environ and the given "key=value" pairs.
// If a key is in both os.Environ and kv, kv takes precedence.
func environ(kv []string) []string {
	cur := os.Environ()
	new := make([]string, 0, len(cur)+len(kv))

	envs := make(map[string]string, len(cur))
	for _, ev := range cur {
		elem := strings.SplitN(ev, "=", 2)
		if len(elem) != 2 || elem[0] == "" {
			// pass the env var of unusual form untouched.
			// e.g. Windows may have env var names starting with "=".
			new = append(new, ev)
			continue
		}
		if goos == "windows" {
			elem[0] = strings.ToUpper(elem[0])
		}
		envs[elem[0]] = elem[1]
	}
	for _, ev := range kv {
		elem := strings.SplitN(ev, "=", 2)
		if len(elem) != 2 || elem[0] == "" {
			panic(fmt.Sprintf("malformed env var %q from input", ev))
		}
		if goos == "windows" {
			elem[0] = strings.ToUpper(elem[0])
		}
		envs[elem[0]] = elem[1]
	}
	for k, v := range envs {
		new = append(new, k+"="+v)
	}
	return new
}

func getenv(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

func pkgdir(env []string) string {
	return gomobilepath + "/pkg_" + getenv(env, "GOOS") + "_" + getenv(env, "GOARCH")
}

type ndkToolchain struct {
	arch       string
	abi        string
	platform   string
	gcc        string
	toolPrefix string
	minGoVer   goToolVersion
}

func (tc *ndkToolchain) Path(toolName string) string {
	if goos == "windows" {
		toolName += ".exe"
	}
	return filepath.Join(ndk.Root(), tc.arch, "bin", tc.toolPrefix+"-"+toolName)
}

type ndkConfig map[string]ndkToolchain // map: GOOS->androidConfig.

func (nc ndkConfig) Root() string {
	return filepath.Join(gomobilepath, "android-"+ndkVersion)
}

func (nc ndkConfig) Toolchain(arch string) ndkToolchain {
	tc, ok := nc[arch]
	if !ok || tc.minGoVer > goVersion {
		panic(`unsupported architecture: ` + arch)
	}
	return tc
}

// TODO: share this with release.go
var ndk = ndkConfig{
	"arm": {
		arch:       "arm",
		abi:        "armeabi-v7a",
		platform:   "android-15",
		gcc:        "arm-linux-androideabi-4.8",
		toolPrefix: "arm-linux-androideabi",
		minGoVer:   go1_5,
	},
	/*
		        "386": {
		                arch:       "x86",
		                abi:        "x86",
		                platform:   "android-15",
		                gcc:        "x86-4.8",
		                toolPrefix: "i686-linux-android",
				minGoVer: go1_6,

		        },
		        "amd64": {
		                arch:       "x86_64",
		                abi:        "x86_64",
		                platform:   "android-21",
		                gcc:        "x86_64-4.9",
		                toolPrefix: "x86_64-linux-android",
				minGoVer: go1_6,
		        },
	*/
}
