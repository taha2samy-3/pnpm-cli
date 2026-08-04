# Paketo Buildpack for pnpm

The pnpm CNB provides the [pnpm package manager](https://pnpm.io). The
buildpack installs `pnpm` onto the `$PATH` which makes it available for
subsequent buildpacks and/or in the final running container. An example of a
buildpack that might use pnpm is the [pnpm Install
CNB](https://github.com/paketo-buildpacks/pnpm-install).

## Integration

The pnpm CNB provides `pnpm` as a dependency. Downstream buildpacks, like [pnpm
Install CNB](https://github.com/paketo-buildpacks/pnpm-install) can require the
`pnpm` dependency by generating a [Build Plan
TOML](https://github.com/buildpacks/spec/blob/master/buildpack.md#build-plan-toml)
file that looks like the following:

```toml
[[requires]]

  # The name of the pnpm dependency is "pnpm". This value is considered
  # part of the public API for the buildpack and will not change without a plan
  # for deprecation.
  name = "pnpm"

  # The pnpm buildpack supports some non-required metadata options.
  [requires.metadata]

    # The version of the pnpm dependency is not required. In the case it
    # is not specified, the buildpack will provide the default version, which can
    # be seen in the buildpack.toml file.
    # If you wish to request a specific version, the buildpack supports
    # specifying a semver constraint in the form of "9.*", "9.1.*", or even
    # "9.1.0".
    version = "9.1.0"

    # Setting the build flag to true will ensure that the pnpm
    # dependency is available on the $PATH for subsequent buildpacks during
    # their build phase. If you are writing a buildpack that needs to run pnpm
    # during its build process, this flag should be set to true.
    build = true

    # Setting the launch flag to true will ensure that the pnpm
    # dependency is available on the $PATH for the running application. If you are
    # writing an application that needs to run pnpm at runtime, this flag should
    # be set to true.
    launch = true
```

## Usage

To package this buildpack for consumption:

```shell
$ ./scripts/package.sh --version <version-number>
```

This will create a `buildpackage.cnb` file under the `build` directory which you
can use to build your app as follows:
```shell
pack build <app-name> \
  --path <path-to-app> \
  --buildpack <path/to/node-engine.cnb> \
  --buildpack build/buildpackage.cnb \
  --buildpack <path/to/cnb-that-requires-node-and-pnpm>
```

Though the API of this buildpack does not require `node`, pnpm is unusable without node.

## Run Tests

To run all unit tests, run:
```shell
./scripts/unit.sh
```

To run all integration tests, run:
```shell
./scripts/integration.sh
```
