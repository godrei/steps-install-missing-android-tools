package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bitrise-io/go-android/sdk"
	"github.com/bitrise-io/go-android/sdkcomponent"
	"github.com/bitrise-io/go-android/sdkmanager"
	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-steputils/tools"
	"github.com/bitrise-io/go-utils/command"
	"github.com/bitrise-io/go-utils/env"
	"github.com/bitrise-io/go-utils/fileutil"
	"github.com/bitrise-io/go-utils/log"
	"github.com/bitrise-steplib/steps-install-missing-android-tools/androidcomponents"
	"github.com/hashicorp/go-version"
)

const androidNDKHome = "ANDROID_NDK_HOME"

// Config ...
type Config struct {
	GradlewPath    string `env:"gradlew_path,file"`
	AndroidHome    string `env:"ANDROID_HOME"`
	AndroidSDKRoot string `env:"ANDROID_SDK_ROOT"`
	NDKVersion     string `env:"ndk_version"`
}

var logger = log.NewLogger()
var cmdFactory = command.NewFactory(env.NewRepository())

func failf(format string, v ...interface{}) {
	logger.Errorf(format, v...)
	os.Exit(1)
}

// ndkVersion returns the full version string of a given install path
func ndkVersion(ndkPath string) string {
	propertiesPath := filepath.Join(ndkPath, "source.properties")

	content, err := fileutil.ReadStringFromFile(propertiesPath)
	if err != nil {
		return ""
	}

	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(strings.ToLower(line), "pkg.revision") {
			lineParts := strings.Split(line, "=")
			if len(lineParts) == 2 {
				return strings.TrimSpace(lineParts[1])
			}
		}
	}
	return ""
}

func currentNDKHome() string {
	if v := os.Getenv(androidNDKHome); v != "" {
		return v
	}
	if v := os.Getenv("ANDROID_HOME"); v != "" {
		// $ANDROID_HOME is deprecated
		return filepath.Join(v, "ndk-bundle")
	}
	if v := os.Getenv("ANDROID_SDK_ROOT"); v != "" {
		// $ANDROID_SDK_ROOT is preferred over $ANDROID_HOME
		return filepath.Join(v, "ndk-bundle")
	}
	if v := os.Getenv("HOME"); v != "" {
		return filepath.Join(v, "ndk-bundle")
	}
	return "ndk-bundle"
}

// updateNDK installs the requested NDK version (if not already installed to the correct location).
// NDK is installed to the `ndk/version` subdirectory of the SDK location, while updating $ANDROID_NDK_HOME for
// compatibility with older Android Gradle Plugin versions.
// Details: https://github.com/android/ndk-samples/wiki/Configure-NDK-Path
func updateNDK(version string, androidSdk *sdk.Model) error {
	currentNdkHome := currentNDKHome()

	currentVersion := ndkVersion(currentNdkHome)
	if currentVersion == version {
		logger.Donef("NDK %s already installed at %s", version, currentNdkHome)
		return nil
	}

	if currentVersion != "" {
		logger.Printf("NDK %s found at: %s", currentVersion, currentNdkHome)
	}

	logger.Printf("Removing existing NDK...")
	if err := os.RemoveAll(currentNdkHome); err != nil {
		return err
	}
	logger.Printf("Done")

	logger.Printf("Installing NDK %s with sdkmanager", version)
	sdkManager, err := sdkmanager.New(androidSdk, cmdFactory)
	if err != nil {
		return err
	}
	ndkComponent := sdkcomponent.NDK{Version: version}
	cmd := sdkManager.InstallCommand(ndkComponent)
	output, err := cmd.RunAndReturnTrimmedOutput()
	if err != nil {
		logger.Errorf(output)
		return err
	}
	newNDKHome := filepath.Join(androidSdk.GetAndroidHome(), ndkComponent.InstallPathInAndroidHome())

	logger.Printf("Done")

	logger.Printf("Append NDK folder to $PATH")
	// Old NDK folder was deleted above, its path can stay in $PATH
	if err := tools.ExportEnvironmentWithEnvman("PATH", fmt.Sprintf("%s:%s", os.Getenv("PATH"), newNDKHome)); err != nil {
		return err
	}

	if err := tools.ExportEnvironmentWithEnvman(androidNDKHome, newNDKHome); err != nil {
		return err
	}
	logger.Printf("Exported $%s: %s", androidNDKHome, newNDKHome)

	return nil
}

func main() {
	// Input validation
	var config Config
	if err := stepconf.NewInputParser(env.NewRepository()).Parse(&config); err != nil {
		logger.Errorf("%s", err)
	}

	fmt.Println()
	stepconf.Print(config)

	fmt.Println()
	logger.Infof("Preparation")

	// Set executable permission for gradlew
	logger.Printf("Set executable permission for gradlew")
	if err := os.Chmod(config.GradlewPath, 0770); err != nil {
		failf("Failed to set executable permission for gradlew, error: %s", err)
	}

	// Initialize Android SDK
	fmt.Println()
	logger.Infof("Initialize Android SDK")
	androidSdk, err := sdk.NewDefaultModel(sdk.Environment{
		AndroidHome:    config.AndroidHome,
		AndroidSDKRoot: config.AndroidSDKRoot,
	})
	if err != nil {
		failf("Failed to initialize Android SDK: %s", err)
	}

	fmt.Println()
	if config.NDKVersion != "" {
		logger.Infof("Installing Android NDK")

		_, err := version.NewVersion(config.NDKVersion)
		if err != nil {
			failf(fmt.Sprintf("'%s' is not a valid NDK version. This should be the full version number, such as 23.0.7599858. To see all available versions, run 'sdkmanager --list'", config.NDKVersion))
		}

		if err := updateNDK(config.NDKVersion, androidSdk); err != nil {
			failf("Failed to install new NDK package, error: %s", err)
		}
	} else {
		logger.Infof("Clearing NDK environment")
		logger.Printf("Unset ANDROID_NDK_HOME")

		if err := os.Unsetenv("ANDROID_NDK_HOME"); err != nil {
			failf("Failed to unset environment variable, error: %s", err)
		}

		if err := tools.ExportEnvironmentWithEnvman("ANDROID_NDK_HOME", ""); err != nil {
			failf("Failed to set environment variable, error: %s", err)
		}
	}

	// Ensure android licences
	logger.Printf("Ensure android licences")

	if err := androidcomponents.InstallLicences(androidSdk); err != nil {
		failf("Failed to ensure android licences, error: %s", err)
	}

	// Ensure required Android SDK components
	fmt.Println()
	logger.Infof("Ensure required Android SDK components")

	if err := androidcomponents.Ensure(androidSdk, config.GradlewPath); err != nil {
		failf("Failed to ensure android components, error: %s", err)
	}

	fmt.Println()
	logger.Donef("Required SDK components are installed")
}
