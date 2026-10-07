#!/usr/bin/env bash
# Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Generate THIRD_PARTY_NOTICES.md from the dependency graph of the exact
# application build in deployments/container/Dockerfile.distroless:
#
#   CGO_ENABLED=1 go build ./cmd
#
# The graph is collected for linux/amd64 and linux/arm64 and merged. It is not
# derived from every directory in vendor/, so test-only modules are excluded.

set -euo pipefail

OUTPUT="${OUTPUT:-THIRD_PARTY_NOTICES.md}"
GO_LICENSES="${GO_LICENSES:-${PWD}/bin/go-licenses}"
MODULES_TXT="${MODULES_TXT:-vendor/modules.txt}"
MULTI_ARCH_MK="${MULTI_ARCH_MK:-deployments/container/multi-arch.mk}"
DOCKERFILE="${DOCKERFILE:-deployments/container/Dockerfile.distroless}"
PCI_IDS_FILE="${PCI_IDS_FILE:-utils/pci.ids}"
VERSIONS_MK="${VERSIONS_MK:-versions.mk}"
COPYRIGHTS_TSV="${COPYRIGHTS_TSV:-tools/notices/copyrights.tsv}"
RUNTIME_FILES_TEMPLATE="${RUNTIME_FILES_TEMPLATE:-tools/notices/runtime-files.gotmpl}"

SOURCE_REPOSITORY="https://github.com/NVIDIA/kubevirt-gpu-device-plugin"

PACKAGES=("./cmd")
PLATFORMS=(
    "linux/amd64"
    "linux/arm64"
)

PCI_IDS_VERSION="2025.07.11"
PCI_IDS_SHA256="a96cd22db83c10d9141ecd7b14325ed9c936de5ef01d78e7e7e43f070184270b"

# OSRB reviewed this component in the nSpect SBOM and explicitly required its
# file-specific dual licensing to remain in the notices. This is a narrow,
# reviewed supplement to the runtime graph; it is not a scan of all vendor/.
# The dual-license determination was made by reading v3.0.4 specifically, so
# it is pinned to that version and must be re-reviewed before it is trusted
# for any other version.
YAML_MODULE="go.yaml.in/yaml/v3"
YAML_VERSION="v3.0.4"

die() {
    printf 'ERROR: %s\n' "$1" >&2
    shift
    if (( $# > 0 )); then
        printf '%s\n' "$@" >&2
    fi
    exit 1
}

log() {
    printf '%s\n' "$*" >&2
}

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        die "neither sha256sum nor shasum is installed."
    fi
}

fence_for() {
    local file="$1" longest width
    longest=$(LC_ALL=C grep -oaE '`+' "${file}" 2>/dev/null \
        | awk '{ if (length($0) > m) m = length($0) } END { print m+0 }')
    width=$((longest + 1))
    ((width < 3)) && width=3
    printf '%*s' "${width}" '' | tr ' ' '`'
}

check_prerequisites() {
    command -v go >/dev/null 2>&1 || die "go is not installed."
    [[ -x "${GO_LICENSES}" ]] \
        || die "the pinned go-licenses binary is missing at ${GO_LICENSES}." \
            "Install it with 'make install-tools'."

    local file
    for file in \
        "${MODULES_TXT}" \
        "${MULTI_ARCH_MK}" \
        "${DOCKERFILE}" \
        "${PCI_IDS_FILE}" \
        "${VERSIONS_MK}" \
        "${COPYRIGHTS_TSV}" \
        "${RUNTIME_FILES_TEMPLATE}"; do
        [[ -f "${file}" ]] || die "${file} not found; run this command from the repository root."
    done

    LOCAL_MODULE=$(go list -m 2>/dev/null || true)
    [[ -n "${LOCAL_MODULE}" ]] || die "could not determine the local Go module."
}

resolve_release_metadata() {
    RELEASE_VERSION=$(sed -n 's/^VERSION ?= //p' "${VERSIONS_MK}" | head -1)
    [[ "${RELEASE_VERSION}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die \
        "could not determine a release version from ${VERSIONS_MK}."

    BUILDER_IMAGE=$(awk '$1 == "FROM" { print $2; exit }' "${DOCKERFILE}")
    RUNTIME_BASE_IMAGE=$(awk '$1 == "FROM" { image = $2 } END { print image }' "${DOCKERFILE}")
    [[ "${RUNTIME_BASE_IMAGE}" =~ ^nvcr\.io/nvidia/distroless/go:v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die \
        "could not determine the versioned NVIDIA distroless Go runtime base from ${DOCKERFILE}."
    BASE_IMAGE_VERSION="${RUNTIME_BASE_IMAGE##*:}"
    BASE_SOURCE_URL="https://developer.download.nvidia.com/distroless-oss/go/${BASE_IMAGE_VERSION}/index.html"

    APP_SOURCE_URL="${SOURCE_REPOSITORY}/archive/refs/tags/${RELEASE_VERSION}.tar.gz"
    PCI_SOURCE_URL="${SOURCE_REPOSITORY}/blob/${RELEASE_VERSION}/utils/pci.ids"
}

verify_platform_matrix() {
    local expected actual
    expected=$(sed -n 's/^DOCKER_BUILD_PLATFORM_OPTIONS[[:space:]]*?=[[:space:]]*--platform=//p' \
        "${MULTI_ARCH_MK}" | tr ',' '\n' | sed '/^$/d' | LC_ALL=C sort -u)
    [[ -n "${expected}" ]] \
        || die "could not read DOCKER_BUILD_PLATFORM_OPTIONS from ${MULTI_ARCH_MK}."

    actual=$(printf '%s\n' "${PLATFORMS[@]}" | LC_ALL=C sort -u)
    [[ "${expected}" == "${actual}" ]] || die \
        "the notices platform matrix differs from the image platform matrix." \
        "  notices: $(echo "${actual}" | paste -sd ' ' -)" \
        "  images:  $(echo "${expected}" | paste -sd ' ' -)"
}

verify_pci_ids() {
    local version checksum
    version=$(sed -n 's/^#[[:space:]]*Version:[[:space:]]*//p' "${PCI_IDS_FILE}" | head -1)
    checksum=$(sha256_file "${PCI_IDS_FILE}")

    [[ "${version}" == "${PCI_IDS_VERSION}" ]] || die \
        "${PCI_IDS_FILE} version is ${version:-unknown}; expected ${PCI_IDS_VERSION}." \
        "Update the reviewed pci.ids metadata before regenerating notices."
    [[ "${checksum}" == "${PCI_IDS_SHA256}" ]] || die \
        "${PCI_IDS_FILE} SHA-256 is ${checksum}; expected ${PCI_IDS_SHA256}." \
        "Update the reviewed pci.ids metadata before regenerating notices."

    LC_ALL=C grep -q '3-clause BSD License' "${PCI_IDS_FILE}" \
        || die "${PCI_IDS_FILE} no longer advertises the selected BSD-3-Clause option."
    LC_ALL=C grep -q 'Martin Mares and Albert Pool' "${PCI_IDS_FILE}" \
        || die "${PCI_IDS_FILE} no longer contains the reviewed copyright holders."
    LC_ALL=C grep -q 'aggregation and formatting' "${PCI_IDS_FILE}" \
        || die "${PCI_IDS_FILE} no longer contains the reviewed copyright scope."
}

prepare_workspace() {
    local tmp_base out_dir
    tmp_base="${TMPDIR:-/tmp}/kubevirt-gpu-device-plugin-notices"
    WORK_DIR=$(mktemp -d "${tmp_base}.XXXXXX")
    PKG_MODULES="${WORK_DIR}/package-modules.csv"
    LICENSE_CSV="${WORK_DIR}/licenses.csv"
    MODULE_INDEX="${WORK_DIR}/module-index.csv"
    RUNTIME_MODULES="${WORK_DIR}/runtime-modules.csv"
    MODULE_LICENSES="${WORK_DIR}/module-licenses.csv"
    RUNTIME_FILES_RAW="${WORK_DIR}/runtime-files-raw.tsv"
    RUNTIME_FILES="${WORK_DIR}/runtime-files.tsv"
    mkdir -p "${WORK_DIR}/go-build"
    export GOCACHE="${GOCACHE:-${WORK_DIR}/go-build}"

    out_dir=$(dirname "${OUTPUT}")
    mkdir -p "${out_dir}"
    OUT_TMP=$(mktemp "${out_dir}/.$(basename "${OUTPUT}").XXXXXX")
    trap 'rm -rf "${WORK_DIR}"; rm -f "${OUT_TMP}"' EXIT
}

collect_runtime_graph() {
    local platform goos goarch source_files_template
    export GOFLAGS="-mod=vendor"
    export CGO_ENABLED=1

    # Record only files selected by go list for the exact target. Test and
    # ignored files are deliberately absent. These records let reviewed
    # copyright metadata be checked against code that is actually compiled or
    # embedded into an image build.
    source_files_template=$(<"${RUNTIME_FILES_TEMPLATE}")

    for platform in "${PLATFORMS[@]}"; do
        goos="${platform%/*}"
        goarch="${platform#*/}"
        log "Collecting ${PACKAGES[*]} for ${goos}/${goarch} with CGO_ENABLED=1..."

        GOOS="${goos}" GOARCH="${goarch}" go list -deps \
            -f '{{if and (not .Standard) .Module}}{{.ImportPath}},{{.Module.Path}}{{end}}' \
            "${PACKAGES[@]}" \
            | awk -F, -v local="${LOCAL_MODULE}" 'NF == 2 && $2 != local { print }' \
            >> "${PKG_MODULES}"

        GOOS="${goos}" GOARCH="${goarch}" go list -deps \
            -f "${source_files_template}" "${PACKAGES[@]}" \
            | sed '/^$/d' >> "${RUNTIME_FILES_RAW}"

        # In vendor mode go-licenses cannot construct useful source URLs from
        # this repository's intentionally short module path. URLs are not used
        # here (module comes from go list), so keep those warnings in a
        # diagnostic file and print them only if classification itself fails.
        if ! GOOS="${goos}" GOARCH="${goarch}" "${GO_LICENSES}" csv "${PACKAGES[@]}" \
            --ignore="${LOCAL_MODULE}" \
            >> "${LICENSE_CSV}" 2> "${WORK_DIR}/go-licenses-${goos}-${goarch}.log"; then
            cat "${WORK_DIR}/go-licenses-${goos}-${goarch}.log" >&2
            die "go-licenses failed for ${goos}/${goarch}."
        fi
    done

    LC_ALL=C sort -u "${PKG_MODULES}" -o "${PKG_MODULES}"
    LC_ALL=C sort -u "${LICENSE_CSV}" -o "${LICENSE_CSV}"

    # Convert import-path/file pairs to paths relative to each module root so
    # the reviewed manifest is stable across checkout locations.
    awk -F '\t' 'BEGIN { OFS = "\t" }
        NF == 3 {
            if ($2 == $1) {
                relative = $3
            } else if (index($2, $1 "/") == 1) {
                relative = substr($2, length($1) + 2) "/" $3
            } else {
                print "runtime package " $2 " is outside module " $1 > "/dev/stderr"
                bad = 1
                next
            }
            print $1, relative
        }
        END { exit bad }
    ' "${RUNTIME_FILES_RAW}" | LC_ALL=C sort -u > "${RUNTIME_FILES}"

    [[ -s "${PKG_MODULES}" ]] || die "the runtime package graph is empty."
    [[ -s "${LICENSE_CSV}" ]] || die "go-licenses produced an empty license inventory."
    [[ -s "${RUNTIME_FILES}" ]] || die "the runtime source-file graph is empty."
}

build_module_index() {
    cut -d, -f2 "${PKG_MODULES}" | LC_ALL=C sort -u > "${RUNTIME_MODULES}"

    # go-licenses reports the directory that owns the selected legal file. It
    # can therefore return a module root (github.com/gogo/protobuf) even when
    # go list reports only imported subpackages. Map each report row to the
    # longest runtime module-path prefix instead of requiring an exact package
    # name match.
    awk -F, '
        NR == FNR {
            modules[++count] = $1
            next
        }
        $3 != "" {
            best = ""
            for (i = 1; i <= count; i++) {
                module = modules[i]
                if (($1 == module || index($1, module "/") == 1) && length(module) > length(best)) {
                    best = module
                }
            }
            if (best != "") print best "," $3
        }
    ' "${RUNTIME_MODULES}" "${LICENSE_CSV}" \
        | LC_ALL=C sort -u > "${MODULE_LICENSES}"

    local module
    while IFS= read -r module; do
        LC_ALL=C grep -q "^${module}," "${MODULE_LICENSES}" \
            || die "go-licenses did not classify runtime module ${module}."
    done < "${RUNTIME_MODULES}"

    awk -F, '
        {
            key = $1
            if (key != previous) {
                if (NR > 1) print module "," licenses ",runtime graph"
                module = $1
                licenses = $2
                previous = key
            } else {
                licenses = licenses " AND " $2
            }
        }
        END {
            if (NR > 0) print module "," licenses ",runtime graph"
        }
    ' "${MODULE_LICENSES}" > "${MODULE_INDEX}"

    local vendored_yaml_version
    vendored_yaml_version=$(awk -v module="${YAML_MODULE}" '$1 == "#" && $2 == module { print $3; exit }' "${MODULES_TXT}")
    [[ "${vendored_yaml_version}" == "${YAML_VERSION}" ]] || die \
        "reviewed ${YAML_MODULE} version is ${YAML_VERSION}, but vendor/modules.txt has ${vendored_yaml_version:-none}."

    # Preserve the reviewed file-specific dual-license result even if generic
    # classifiers choose one match from the combined upstream LICENSE file.
    awk -F, -v yaml="${YAML_MODULE}" 'BEGIN { OFS = "," }
        $1 == yaml { $2 = "Apache-2.0 AND MIT"; $3 = "runtime graph; OSRB/nSpect dual-license override" }
        { print }
    ' "${MODULE_INDEX}" > "${MODULE_INDEX}.tmp"
    mv "${MODULE_INDEX}.tmp" "${MODULE_INDEX}"

    if ! cut -d, -f1 "${MODULE_INDEX}" | LC_ALL=C grep -qx "${YAML_MODULE}"; then
        printf '%s,%s,%s\n' \
            "${YAML_MODULE}" "Apache-2.0 AND MIT" \
            "OSRB/nSpect reviewed supplement" >> "${MODULE_INDEX}"
    fi

    LC_ALL=C sort -u "${MODULE_INDEX}" -o "${MODULE_INDEX}"
}

license_files_for_module() {
    local module="$1" root="vendor/$1" file
    [[ -d "${root}" ]] || return 0
    while IFS= read -r -d '' file; do
        if printf '%s' "$(basename "${file}")" \
            | LC_ALL=C grep -qiE '^(licen[cs]e|notice|copying|copyright|authors|patents)([-._].*)?$'; then
            printf '%s\n' "${file}"
        fi
    done < <(find "${root}" -maxdepth 1 -type f -print0 | LC_ALL=C sort -z)
}

copyrights_for_module() {
    local module="$1"
    awk -F '\t' -v module="${module}" \
        '$1 == module { print $3 }' "${COPYRIGHTS_TSV}" \
        | LC_ALL=C sort -u
}

runtime_copyrights_for_module() {
    local module="$1" mapped_module source source_file
    while IFS=$'\t' read -r mapped_module source; do
        [[ "${mapped_module}" == "${module}" ]] \
            || continue
        source_file="vendor/${module}/${source}"
        [[ -f "${source_file}" ]] || continue
        LC_ALL=C awk '
            NR <= 50 {
                line = $0
                sub(/^[[:space:]]*(\/\/|#|\/\*|\*)[[:space:]]*/, "", line)
                sub(/[[:space:]]*\*\/[[:space:]]*$/, "", line)
                if (line ~ /^Copyright[[:space:]]+(\(c\)|©|[0-9][0-9][0-9][0-9]|The[[:space:]])/) {
                    print line
                }
            }
        ' "${source_file}"
    done < "${RUNTIME_FILES}" | LC_ALL=C sort -u
}

legal_files_have_real_copyright() {
    local module="$1" file
    while IFS= read -r file; do
        [[ -n "${file}" ]] || continue
        # Exclude the Apache appendix placeholder. The accepted forms cover
        # the actual upstream notices in the reviewed dependency set; other
        # forms must be added to the reviewed manifest explicitly.
        if LC_ALL=C grep -Eqi \
            '^[[:space:]]*Copyright[[:space:]]+(\(c\)|©|[0-9]{4}|The[[:space:]])' \
            "${file}"; then
            return 0
        fi
    done < <(license_files_for_module "${module}")
    return 1
}

validate_copyright_metadata() {
    local malformed duplicate module source notice source_file runtime_record
    local reviewed_notices runtime_notices

    malformed=$(awk -F '\t' '!/^#/ && NF && NF != 3 { print NR }' "${COPYRIGHTS_TSV}" \
        | paste -sd, -)
    [[ -z "${malformed}" ]] \
        || die "${COPYRIGHTS_TSV} must contain three tab-separated fields; malformed line(s): ${malformed}."

    duplicate=$(awk -F '\t' '
        !/^#/ && NF == 3 {
            key = $1 SUBSEP $3
            if (++seen[key] == 2) print $1 ": " $3
        }
    ' "${COPYRIGHTS_TSV}" | head -1)
    [[ -z "${duplicate}" ]] \
        || die "duplicate reviewed copyright notice in ${COPYRIGHTS_TSV}: ${duplicate}"

    while IFS=$'\t' read -r module source notice; do
        [[ -n "${module}" && "${module}" != \#* ]] || continue
        [[ -n "${source}" && -n "${notice}" ]] \
            || die "incomplete reviewed copyright entry for ${module}."
        [[ "${notice}" == Copyright* ]] \
            || die "reviewed notice for ${module} is not a copyright line: ${notice}"
        [[ "${notice}" != *'Copyright [yyyy]'* ]] \
            || die "the Apache copyright placeholder is not a component attribution."

        LC_ALL=C grep -Fqx "${module}" "${RUNTIME_MODULES}" \
            || die "reviewed copyright entry is outside the runtime graph: ${module}."

        runtime_record=$(printf '%s\t%s' "${module}" "${source}")
        LC_ALL=C grep -Fqx "${runtime_record}" "${RUNTIME_FILES}" \
            || die "reviewed copyright source is not selected by either image build: ${module}/${source}."

        source_file="vendor/${module}/${source}"
        [[ -f "${source_file}" ]] \
            || die "reviewed copyright source does not exist: ${source_file}."
        LC_ALL=C grep -Fq -- "${notice}" "${source_file}" \
            || die "reviewed copyright notice is stale in ${source_file}: ${notice}"
    done < "${COPYRIGHTS_TSV}"

    # For every module that needs supplemental attribution, the reviewed set
    # must equal all distinct copyright headers found in the first 50 lines of
    # its selected runtime files. This catches additions as well as stale
    # manifest entries without scanning unrelated packages under vendor/.
    while IFS= read -r module; do
        [[ -n "${module}" && "${module}" != \#* ]] || continue
        reviewed_notices=$(copyrights_for_module "${module}")
        runtime_notices=$(runtime_copyrights_for_module "${module}")
        if [[ "${reviewed_notices}" != "${runtime_notices}" ]]; then
            printf 'Reviewed and runtime copyright notices differ for %s:\n' \
                "${module}" >&2
            diff -u \
                <(printf '%s\n' "${reviewed_notices}") \
                <(printf '%s\n' "${runtime_notices}") >&2 || true
            die "update ${COPYRIGHTS_TSV} after reviewing the compiled copyright headers."
        fi
    done < <(awk -F '\t' '!/^#/ && NF == 3 { print $1 }' \
        "${COPYRIGHTS_TSV}" | LC_ALL=C sort -u)

    # Every component must reproduce a real copyright either in its upstream
    # top-level legal files or through the compiled-file manifest above.
    while IFS=, read -r module _; do
        if legal_files_have_real_copyright "${module}"; then
            continue
        fi
        [[ -n "$(copyrights_for_module "${module}")" ]] \
            || die "${module} has no component copyright attribution."
    done < "${MODULE_INDEX}"
}

validate_legal_files() {
    local module licenses basis count
    while IFS=, read -r module licenses basis; do
        count=$(license_files_for_module "${module}" | wc -l | tr -d ' ')
        ((count > 0)) || die "no top-level legal files found for ${module}."
    done < "${MODULE_INDEX}"

    local yaml_license="vendor/${YAML_MODULE}/LICENSE"
    local yaml_notice="vendor/${YAML_MODULE}/NOTICE"
    [[ -f "${yaml_license}" && -f "${yaml_notice}" ]] \
        || die "${YAML_MODULE} must retain both LICENSE and NOTICE."
    LC_ALL=C grep -q 'Copyright (c) 2006-2010 Kirill Simonov' "${yaml_license}" \
        || die "${YAML_MODULE}/LICENSE is missing the 2006-2010 Kirill Simonov copyright."
    LC_ALL=C grep -q 'Copyright (c) 2006-2011 Kirill Simonov' "${yaml_license}" \
        || die "${YAML_MODULE}/LICENSE is missing the 2006-2011 Kirill Simonov copyright."
    LC_ALL=C grep -q 'Copyright (c) 2011-2019 Canonical Ltd' "${yaml_license}" \
        || die "${YAML_MODULE}/LICENSE is missing the Canonical Ltd copyright."
    LC_ALL=C grep -q 'MIT License' "${yaml_license}" \
        || die "${YAML_MODULE}/LICENSE no longer contains its MIT terms."
    LC_ALL=C grep -q 'Apache License' "${yaml_license}" \
        || die "${YAML_MODULE}/LICENSE no longer contains its Apache-2.0 terms."
}

module_version() {
    local module="$1"
    awk -v module="${module}" '$1 == "#" && $2 == module { print $3; exit }' "${MODULES_TXT}"
}

module_source_base() {
    local module="$1" version revision
    version=$(module_version "${module}")
    [[ -n "${version}" ]] || die "could not determine the vendored version for ${module}."
    revision="${version##*-}"
    # Go pseudo-versions are not upstream Git tags.
    if [[ "${version}" =~ [.-][0-9]{14}-[0-9a-f]{12}$ ]]; then
        version="${revision}"
    fi

    case "${module}" in
        github.com/NVIDIA/go-nvfm)
            printf 'https://github.com/NVIDIA/go-nvfm/blob/%s/' "${revision}"
            ;;
        github.com/NVIDIA/go-nvml)
            printf 'https://github.com/NVIDIA/go-nvml/blob/%s/' "${version}"
            ;;
        github.com/NVIDIA/gpu-monitoring-tools)
            printf 'https://github.com/NVIDIA/gpu-monitoring-tools/blob/%s/' "${revision}"
            ;;
        github.com/fsnotify/fsnotify)
            printf 'https://github.com/fsnotify/fsnotify/blob/%s/' "${version}"
            ;;
        github.com/go-logr/logr)
            printf 'https://github.com/go-logr/logr/blob/%s/' "${version}"
            ;;
        github.com/gogo/protobuf)
            printf 'https://github.com/gogo/protobuf/blob/%s/' "${version}"
            ;;
        go.yaml.in/yaml/v3)
            printf 'https://github.com/yaml/go-yaml/blob/%s/' "${version}"
            ;;
        golang.org/x/net)
            printf 'https://github.com/golang/net/blob/%s/' "${version}"
            ;;
        golang.org/x/sys)
            printf 'https://github.com/golang/sys/blob/%s/' "${version}"
            ;;
        golang.org/x/text)
            printf 'https://github.com/golang/text/blob/%s/' "${version}"
            ;;
        google.golang.org/genproto/googleapis/rpc)
            printf 'https://github.com/googleapis/go-genproto/blob/%s/' "${revision}"
            ;;
        google.golang.org/grpc)
            printf 'https://github.com/grpc/grpc-go/blob/%s/' "${version}"
            ;;
        google.golang.org/protobuf)
            printf 'https://github.com/protocolbuffers/protobuf-go/blob/%s/' "${version}"
            ;;
        k8s.io/klog/v2)
            printf 'https://github.com/kubernetes/klog/blob/%s/' "${version}"
            ;;
        k8s.io/kubelet)
            printf 'https://github.com/kubernetes/kubelet/blob/%s/' "${version}"
            ;;
        *)
            die "no reviewed upstream license URL mapping for ${module}."
            ;;
    esac
}

# Build one registry for both the summary and reference list. Legal files and
# versions come from the selected inventory, never a second hand-maintained list.
build_license_references() {
    LICENSE_REFERENCES="${WORK_DIR}/license-references.tsv"
    local module licenses basis source_base file count index=0 ordinal suffix id
    : > "${LICENSE_REFERENCES}"
    while IFS=, read -r module licenses basis; do
        index=$((index + 1))
        source_base=$(module_source_base "${module}")
        count=$(license_files_for_module "${module}" | wc -l | tr -d ' ')
        (( count > 0 && count <= 26 )) || die "unsupported legal-file count for ${module}: ${count}"
        ordinal=0
        while IFS= read -r file; do
            suffix=""
            if (( count > 1 )); then
                suffix=$(printf '%s' abcdefghijklmnopqrstuvwxyz | cut -c "$((ordinal + 1))")
            fi
            printf -v id 'L%02d%s' "${index}" "${suffix}"
            printf '%s\t%s\t%s%s\n' "${module}" "${id}" "${source_base}" "$(basename "${file}")" >> "${LICENSE_REFERENCES}"
            ordinal=$((ordinal + 1))
        done < <(license_files_for_module "${module}")
    done < "${MODULE_INDEX}"
    printf 'pci.ids\tL%02d\t%s#L12-L18\n' "$((index + 1))" "${PCI_SOURCE_URL}" >> "${LICENSE_REFERENCES}"
}

references_for_module() {
    awk -F '\t' -v module="$1" '$1 == module { printf "%s%s", sep, $2; sep=", " }' "${LICENSE_REFERENCES}"
}

emit_index() {
    local module licenses basis version
    printf '| Package | Version | License summary | License file |\n'
    printf '|---------|---------|-----------------|--------------|\n'
    while IFS=, read -r module licenses basis; do
        version=$(module_version "${module}")
        printf '| `%s` | `%s` | %s | %s |\n' \
            "${module}" "${version}" "${licenses}" "$(references_for_module "${module}")"
    done < "${MODULE_INDEX}"
    printf '| `pci.ids` | `%s` | BSD-3-Clause (selected from GPL-2.0-or-later OR BSD-3-Clause) | %s |\n' \
        "${PCI_IDS_VERSION}" "$(references_for_module pci.ids)"
}

emit_license_file_references() {
    awk -F '\t' '{ printf "%-6s%s\n", $2, $3 }' "${LICENSE_REFERENCES}"
}

emit_module_sections() {
    local module licenses basis version source_base file fence copyright_notices
    while IFS=, read -r module licenses basis; do
        version=$(module_version "${module}")
        source_base=$(module_source_base "${module}")

        printf '### %s\n\n' "${module}"
        printf '* Version: `%s`\n' "${version}"
        printf '* License: %s\n' "${licenses}"
        printf '* Location: [LICENSE](%sLICENSE)\n' "${source_base}"
        printf '* Bundled source: `vendor/%s`\n\n' "${module}"

        copyright_notices=$(copyrights_for_module "${module}")
        if [[ -n "${copyright_notices}" ]]; then
            printf '#### Copyright notices\n\n'
            printf '```text\n%s\n```\n\n' "${copyright_notices}"
        fi

        while IFS= read -r file; do
            [[ -n "${file}" ]] || continue
            fence=$(fence_for "${file}")
            printf '#### [%s](%s%s)\n\n' \
                "$(basename "${file}")" "${source_base}" "$(basename "${file}")"
            printf '%stext\n' "${fence}"
            cat "${file}"
            printf '\n%s\n\n' "${fence}"
        done < <(license_files_for_module "${module}")
    done < "${MODULE_INDEX}"
}
emit_pci_ids() {
    cat <<EOF
### pci.ids

* Name: pci.ids
* Type: data
* Installed path: \`/usr/pci.ids\`
* Version: \`${PCI_IDS_VERSION}\`
* Chosen license: BSD-3-Clause
* SHA-256: \`${PCI_IDS_SHA256}\`
* Source and license notice: [PCI ID database shipped with ${RELEASE_VERSION}](${PCI_SOURCE_URL}#L1-L18)
* Original project: [PCI ID Project](https://pci-ids.ucw.cz/)
* Copyright holders: Martin Mares and Albert Pool

The PCI ID Project offers this file under GPL-2.0-or-later OR BSD-3-Clause.
NVIDIA elects the BSD-3-Clause option for this distribution.

#### Original PCI ID Project notice

\`\`\`text
EOF
    sed -n '1,/^#[[:space:]]*Martin Mares and Albert Pool\.$/p' "${PCI_IDS_FILE}"
    cat <<'EOF'
```

#### BSD-3-Clause license

```text
Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice,
   this list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.

3. Neither the name of the copyright holder nor the names of its contributors
   may be used to endorse or promote products derived from this software
   without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```
EOF
}

compose_document() {
    log "Composing ${OUTPUT}..."
    {
        printf '# Third-Party Notices\n\nNVIDIA KubeVirt GPU Device Plugin %s\n\n' "${RELEASE_VERSION}"
        cat <<'EOF'
Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.

This document reproduces notices for the application-layer third-party Go
modules and shipped `pci.ids` data in the NVIDIA KubeVirt GPU Device Plugin
container image. It is not a complete inventory of the container's software.
The dependency inventory is based on the union of the application package
graphs selected by the image's exact build command,
`CGO_ENABLED=1 go build ./cmd`, for `linux/amd64` and `linux/arm64`. Go
test-only packages are excluded. Go standard-library code is included in the
compiled executable but is outside the third-party Go module inventory below.
Notice coverage for Go runtime/standard-library code, other native code, and
base-image packages requires separate review; it is not established by this
application-module inventory or the base-image source index.

The `License file` identifiers in the third-party software summary map to the
upstream legal files for the exact component versions. Corresponding source
for application-layer components is present under `vendor/` in the release
source archive.

`go.yaml.in/yaml/v3` is retained as a narrow OSRB/nSpect-reviewed supplement.
Its files ported from libyaml are MIT licensed; the remaining files are
Apache-2.0 licensed. Both licenses apply to different files, so its
classification is `Apache-2.0 AND MIT`.

`pci.ids` is a shipped data file outside the Go module graph. The PCI ID
Project offers it under GPL-2.0-or-later OR BSD-3-Clause; NVIDIA selects
BSD-3-Clause for this distribution.

EOF
        printf 'The [%s source archive](%s) contains the exact vendored source and legal files used here.\n\n' \
            "${RELEASE_VERSION}" "${APP_SOURCE_URL}"
        cat <<'EOF'
## Container Base Image

EOF
        printf 'The final runtime image is built from `%s`. Its operating-system packages are outside the Go dependency inventory below. The [NVIDIA Distroless OSS source index](%s) provides version-specific package sources and binaries. This source index is not a substitute for the base image\x27s license and notice texts.\n\n' \
            "${RUNTIME_BASE_IMAGE}" "${BASE_SOURCE_URL}"
        printf 'The build stage uses `%s`, but that builder filesystem is not redistributed in the final image; only the compiled application and pci.ids data file are copied from that stage.\n\n' \
            "${BUILDER_IMAGE}"
        printf '| Image | Version | Role | Notices and source |\n'
        printf '|-------|---------|------|--------------------|\n'
        printf '| `nvcr.io/nvidia/distroless/go` | `%s` | final runtime base | [NVIDIA Distroless OSS source index](%s) |\n\n' \
            "${BASE_IMAGE_VERSION}" "${BASE_SOURCE_URL}"
        cat <<'EOF'
## Third-Party Software Summary

EOF
        emit_index
        cat <<'EOF'

## License-file references

The identifiers in the `License file` column resolve to the exact upstream
legal files for the redistributed versions. These external locations are
supplemental; the corresponding legal texts are reproduced below.

```text
EOF
        emit_license_file_references
        cat <<'EOF'
```

## Third-Party Software License and Notice Texts

EOF
        emit_module_sections
        printf '\n## Shipped Data File License\n\n'
        emit_pci_ids
    } > "${OUT_TMP}"

    chmod 644 "${OUT_TMP}"
    mv "${OUT_TMP}" "${OUTPUT}"
}

main() {
    check_prerequisites
    resolve_release_metadata
    verify_platform_matrix
    verify_pci_ids
    prepare_workspace
    collect_runtime_graph
    build_module_index
    validate_legal_files
    validate_copyright_metadata
    build_license_references
    compose_document

    local runtime_count total_count
    runtime_count=$(awk -F, '$3 ~ /^runtime graph/ { count++ } END { print count+0 }' "${MODULE_INDEX}")
    total_count=$(wc -l < "${MODULE_INDEX}" | tr -d ' ')
    log "Wrote ${OUTPUT} (${runtime_count} runtime modules, ${total_count} reviewed Go modules, plus pci.ids)"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
