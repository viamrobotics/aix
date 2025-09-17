# AIX
AppImage eXtender is a tool for use in AppImage packages, allowing self-updates, including seemless update-then-run, and support for selecting optional install (or other) scripts/payloads.
In other words, AIX is an AppImage update tool combined with a target-payload selector.


## tl;dr
```
go install github.com/viamrobotics/aix@latest
cp `go env GOPATH`/bin/aix "$APPDIR/usr/bin/"
```
Set your AppImage environment to run the aix binary as the target and make sure your AppImage sets the environment variable "AIX_TARGET" to the (relative) path of the default target (original payload.)

## AIX Options
All CLI options are prefaced with --aix- so as to not overlap with arguments for the actual payload. These are stripped from the ARGS list before the target is run, so it should have no indication AIX was in use, save for the AIX_TARGET env variable.

### Update
`./test.AppImage --aix-update`
This will self-update the AppImage using the .upd_info section (per the AppImage spec) and then exit. Currently only zsync is supported.

### Auto Update
`./test.AppImage --aix-auto-update --other-args --go to/the/payload`
This will check for an update, and if one is available, update, then execute the new version. If no update is found, execution just continues normally.

### Custom Target
`./test.AppImage --aix-target=aix.d/my_script.sh`
This will run the file aix.d/my_script.sh instead of the main payload (the file set in AIX_TARGET)
Note that the path should be relative to the APPDIR, as $APPDIR will be prepended before execution.

### Install Target
`./test.AppImage --aix-install`
This is just a shortcut for
`./test.AppImage --aix-target=aix.d/install`
It is expected this will be a script or program that will "install" the AppImage. For example, move it to /usr/local/bin/myApp and install systemd service files to start on boot.


## AppImage Implementation Details
AppImages are effectively a small "runtime" executable appended with a squashfs filesystem image. The runtime mounts the image at /tmp/.mountXXXXXXXXX (random string) and then executes the binary named "AppRun" from within the mounted directory. When execution finishes, the runtime unmounts the image.
AIX can directly replace AppRun in some situations (where a target will be provided on the command line), or can be the target of a more complex AppRun (such as that used by appimage-builder.)

When using appimage-builder, binaries in an AppImage are patched at image creation time to use libraries (including libc and ld-linux) at specific known locations. At runtime, the AppRun selects a libc from the newer of the image or the host system, and links are created to run with the chosen version. Other libraries are simply included from the AppImage as the environment (such as LD_LIBRARY_PATH) and some functions (like exec()) are hooked by libappimage_hook.so (injected via LD_PRELOAD.)


## TODO
- Add support for reading an environment/settings file directly, to better work as a full AppRun replacement.
