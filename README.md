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


## Version Selection

The pnpm CNB resolves the requested `pnpm` version using a deterministic 4-tier order of precedence:

1. **Environment Variable Override (`BP_PNPM_VERSION`)**: If set (e.g., `BP_PNPM_VERSION=10.34.5`, `BP_PNPM_VERSION=9.*`, or `BP_PNPM_VERSION=^9.1.0`), the buildpack enforces this explicit version or semver range with highest priority.
2. **`package.json` Configuration**: If not overridden by the environment, the buildpack inspects `package.json` for the `packageManager` key (e.g., `"packageManager": "pnpm@9.15.9"` or `"packageManager": "pnpm@11.x"`).
3. **Lockfile Auto-detection (`pnpm-lock.yaml`)**: If no version is specified via environment or `package.json`, the buildpack parses `pnpm-lock.yaml` for `lockfileVersion` and dynamically maps it to the corresponding major release stream:
   * Lockfile `5.x` $\rightarrow$ Resolves to highest release in **PNPM Major 7**.
   * Lockfile `6.x` $\rightarrow$ Resolves to highest release in **PNPM Major 8**.
   * Lockfile `N.x` ($N \ge 9$) $\rightarrow$ Resolves to highest release in **PNPM Major N** (e.g., Lockfile `9.x` $\rightarrow$ Major `9`, Lockfile `10.x` $\rightarrow$ Major `10`, Lockfile `11.x` $\rightarrow$ Major `11`, Lockfile `12.x` $\rightarrow$ Major `12`).
4. **Default Fallback**: If no version preference is specified, the buildpack dynamically defaults to the highest **stable release in Major family 9** (specifically `9.15.9`). Unstable pre-releases (`-alpha`, `-beta`, `-rc`) are safely bypassed for default selection.

---

### Supported Version Patterns & Ranges

The buildpack features an advanced Semver Pattern Resolution Engine (`detect.go`) capable of parsing and matching any standard semver constraint, wildcard, or incomplete input pattern against the embedded dependency catalog:

| Pattern Type | Input Example | Resolution Behavior | Example Result |
| :--- | :--- | :--- | :--- |
| **Exact Version** | `9.15.9`, `pnpm@10.34.5` | Matches the exact version string directly. | `9.15.9` |
| **Major Wildcard** | `9`, `9.x`, `9.*`, `pnpm@9` | Resolves to the highest available release within Major family 9. | `9.15.9` |
| **Minor Wildcard** | `9.1`, `9.1.x`, `9.1.*` | Resolves to the highest available release strictly within Minor branch 9.1. | `9.1.4` |
| **Incomplete Pattern** | `9.`, `v9.` | Auto-sanitized to `9.*` and resolves to highest release in Major family 9. | `9.15.9` |
| **Caret Range** | `^9.1.0` | Resolves to the highest compatible release without incrementing the major version (`<10.0.0`). | `9.15.9` |
| **Tilde Range** | `~9.1.0` | Resolves to the highest patch update strictly within the same minor branch (`<9.2.0`). | `9.1.4` |
| **Comparison Range** | `>=9.0.0 <11.0.0` | Evaluates multi-bound constraints and selects the highest matching release. | `10.34.5` |

---

### Resolution & Fallback Algorithm

When resolving any requested version string or pattern, the buildpack executes the following resolution pipeline:

```
[Input Version String] 
          │
          ▼
   1. Sanitize Pattern (Strip 'pnpm@', 'v', trim whitespace, fix trailing dots)
          │
          ▼
   2. Direct Exact Match Check (Exact string match in embedded JSON manifest)
          │
          ├─► [Match Found] ──► Return Exact Version
          ▼
   3. Semver Pattern / Constraint Evaluation (Matches wildcards, caret, tilde)
          │
          ├─► [Match Found] ──► Return Highest Matching Version
          ▼
   4. Major Family Fallback (Parses major integer -> searches highest in major family)
          │
          ├─► [Match Found] ──► Return Highest Version in Major Family
          ▼
   5. buildpack.toml / Online Registry Fallback
          │
          ├─► [Offline] ──► Resolves pre-cached tarball from buildpack.toml
          └─► [Online]  ──► Queries https://registry.npmjs.org/pnpm for live fetch
```

---

### Supported Embedded Manifest & Architectures

The buildpack embeds a complete historical dependency catalog covering **ALL published pnpm releases** from the official npm registry for Linux OS:

| Major Series | Included Versions in Embedded Manifest | Architectures | Operating System |
| :--- | :--- | :---: | :---: |
| **PNPM v12.x** | Release Candidates & Pre-releases (`12.0.0-rc.0`, alphas, betas) | `amd64` / `arm64` | `linux` |
| **PNPM v11.x** | All published releases (`11.0.0` $\rightarrow$ `11.20.0`) | `amd64` / `arm64` | `linux` |
| **PNPM v10.x** | All published releases (`10.1.0` $\rightarrow$ `10.34.5`) | `amd64` / `arm64` | `linux` |
| **PNPM v9.x** | All published releases (Default fallback: `9.15.9`) | `amd64` / `arm64` | `linux` |
| **PNPM v8.x** | All published releases (e.g., `8.15.9`, `8.15.4`) | `amd64` / `arm64` | `linux` |
| **PNPM v1.x – v7.x** | Full historical catalog from npm (`0.1.0` $\rightarrow$ `7.33.6`) | `amd64` / `arm64` | `linux` |


> [!NOTE]
> **Manifest Note**: The embedded `dependencies.json` contains every valid semver release published on npm. Core stable target releases (`9.15.9`, `10.34.5`, `11.20.0`) are also pre-cached in `buildpack.toml` for zero-network air-gapped environment builds.



