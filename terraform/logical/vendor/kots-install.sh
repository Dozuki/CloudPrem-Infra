#!/bin/bash

# Installer script for kots version v1.100.3.
#
# This script will verify that the environment is suitable for installation before downloading
# and installing kots.
#
# This script can be configured by either setting environment variables or using argument flags,
# but the command line arguments take precedence over the environment variables.
#
# Environment variables:
# ----------------------
#   REPL_USE_SUDO          set this to any value to use sudo when writing to the installation directory.
#
#   REPL_INSTALL_PATH      alternative installation directory to use.
#

READ_TIMEOUT=15
DEFAULT_DIR="/usr/local/bin"
INSECURE="false"
PROG="kots"
RELEASE="v1.100.3"
TMP_DIR=$(mktemp -d -t replicated-XXXXXX)
USER="replicatedhq"

function print_manual_instructions {
  echo ""
	echo "Environment variables to configure this installer:"
	echo "   REPL_INSTALL_PATH=<PATH>  use PATH as the install directory"
	echo "   REPL_USE_SUDO=y           use sudo to install (interactive)"
	echo ""
	echo "To install kots manually, follow these steps:"

	if [[ -z "${URL+x}" ]]; then
		echo "  * Download the appropriate release from https://github.com/replicatedhq/kots/releases"
	else
		echo "  * Download kots with: curl -O ${URL}"
	fi

	case "${FTYPE}" in
		".gz")
			echo "  * Extract the archive with: gzip -d $(basename ${URL})"
			;;
		".tar.gz")
			echo "  * Extract the archive with: tar xvf $(basename ${URL})"
			;;
		".zip")
			echo "  * Extract the archive with: unzip $(basename ${URL})"
			;;
		"")
			;;
		*)
			echo "  * Extract the downloaded release"
	esac

	echo "  * Move and rename the file to a directory in the PATH: mv kots /install/path/kubectl-kots"
	echo "  * Sudo may be required, the install directory can also be any directory in the PATH"
	echo ""
}

# Cleanup temporary files if they exist and return to the starting directory.
# This is trapped on EXIT signals to ensure it is always called on failures.
function cleanup {
	popd &> /dev/null
	if [[ -d "${TMP_DIR}" ]]; then
		rm -rf "${TMP_DIR}"
	fi
}
trap cleanup EXIT

# Print a big error message.
function fail {
	msg="!! Error: $1 !!"
	len="${#msg}"
	border=$(printf "%*s\n" "$len" | tr " " "!")

	echo ""
	echo "$border"
	echo "$msg" 1>&2
	echo "$border"
	print_manual_instructions
	exit 1
}

# Prompt the user if they would like to create the output directory.
function prompt_install_dir {
	if [[ -t 0 || -t /dev/stdin ]]; then
		INPUT="/dev/stdin"
	elif [[ -r /dev/tty ]]; then
		INPUT="/dev/tty"
	fi
	if [[ -z "${INPUT+x}" ]]; then
	  echo "Unable to prompt user for installation directory, using ${DEFAULT_DIR}"
		OUT_DIR="${DEFAULT_DIR}"
		return
	fi

	echo ""
	echo "Please provide the full path to an installation directory that can be written to. If none"
	echo "is provided in ${READ_TIMEOUT} seconds, then ${DEFAULT_DIR} will be used."
	echo ""
	read -p "installation directory [${DEFAULT_DIR}]: " -t ${READ_TIMEOUT} -r REPLY < "${INPUT}"
	echo ""

	if [[ -z "${REPLY:+x}" ]]; then
		echo "No directory given, the default of ${DEFAULT_DIR} will be used"
		OUT_DIR="${DEFAULT_DIR}"
		return
	fi

	OUT_DIR="${REPLY/#~/${HOME}}"
}

# Check that the environment supports the install.
function check_env {
	# Check for and use any environment variables.
	if [[ ! -z "${REPL_INSTALL_PATH:+x}" ]]; then
		OUT_DIR="${REPL_INSTALL_PATH/#~/${HOME}}"
	fi

	if [[ ! -z "${REPL_USE_SUDO:+x}" ]]; then
		USE_SUDO=1
	fi

  # Check that we're running bash
	[[ ! -z "${BASH_VERSION+x}" ]] || fail "Please use bash instead"

	# Check the OS and architecture.
	case $(uname -s) in
		Darwin)
			OS="darwin"
			;;
		Linux)
			OS="linux"
			;;
		*)
			fail "unsupported OS $(uname -s)"
			;;
	esac
	[[ ! -z "${OS+x}" ]] || fail "could not determine the OS"

	case $(uname -m) in
		"amd64" | "x86_64")
			ARCH="amd64"
			;;
		"arm64" | "aarch64")
			ARCH="arm64"
			;;
		"arm")
			ARCH="arm"
			;;
		"i386")
			ARCH="386"
			;;
		*)
			fail "unsupported architecture $(uname -m)"
	esac
	[[ ! -z "${ARCH+x}" ]] || fail "could not determine the architecture"

	# Check for the current OS + arch combination in the available assets.
	# NOTE: the case statements are built by the templating engine by ranging
	#   over the set of assets and creating an OS_ARCH case that assigns that
	#   asset's URL and file type.
	case "${OS}_${ARCH}" in

		darwin_*)
			URL="https://github.com/replicatedhq/kots/releases/download/$RELEASE/kots_darwin_all.tar.gz"
			FTYPE=".tar.gz"
			;;

		linux_amd64)
			URL="https://github.com/replicatedhq/kots/releases/download/$RELEASE/kots_linux_amd64.tar.gz"
			FTYPE=".tar.gz"
			;;

		linux_arm)
			URL="https://github.com/replicatedhq/kots/releases/download/$RELEASE/kots_linux_arm64.tar.gz"
			FTYPE=".tar.gz"
			;;

		*)
			fail "No asset found for platform ${OS}-${ARCH}"
			;;
	esac
	[[ ! -z "${URL+x}" || ! -z "${FTYPE+x}" ]] || fail "could not find a valid release URL for ${OS} ${ARCH}"

	# Check for needed utilities.
	command -v curl &> /dev/null || fail "curl not installed"
	command -v find &> /dev/null || fail "find not installed"
	command -v xargs &> /dev/null || fail "xargs not installed"
	command -v sort &> /dev/null || fail "sort not installed"
	command -v tail &> /dev/null || fail "tail not installed"
	command -v cut &> /dev/null || fail "cut not installed"
	command -v du &> /dev/null || fail "du not installed"

	# Check that the assets can be extracted.
	case "${FTYPE}" in
		".gz")
			command -v gzip &>/dev/null || fail "gzip is not installed"
			;;
		".tar.gz")
			command -v tar &>/dev/null || fail "tar is not installed"
			;;
		".zip")
			command -v unzip &>/dev/null || fail "zip is not installed"
			;;
		"")
			;;
		*)
			fail "unsupported file type ${FTYPE}"
	esac

	# Check if the install directory needs to be prompted for and exists.
	if [[ -z "${OUT_DIR:+x}" ]]; then
		 prompt_install_dir
	fi

	if [[ ! -d "${OUT_DIR}" ]]; then
		if [[ ! -z "${USE_SUDO+x}" ]]; then
			sudo mkdir -p "${OUT_DIR}" &> /dev/null || true
		else
			mkdir -p "${OUT_DIR}" &> /dev/null || true
		fi
	fi

	if [[ ! -w "${OUT_DIR}" && -z "${USE_SUDO+x}" ]]; then
		echo ""
		echo "The installation directory ${OUT_DIR} is not writeable by this user, and installation has failed."
		echo ""
		echo "To fix this, do one of the following:"
		echo "  * Set the environment variable REPL_USE_SUDO to any value and re-run this script. Keep"
		echo "    in mind this script will block waiting on sudo:"
		echo "      curl https://kots.io/install | REPL_USE_SUDO=y bash"
		echo "  * Set the environment variable REPL_INSTALL_PATH to a directory in the PATH that can"
		echo "    be written to and re-run this script:"
		echo "      curl https://kots.io/install | REPL_INSTALL_PATH=/new/path bash"
		echo ""
		fail "cannot write to the installation directory ${OUT_DIR}"
	fi
}

function install {
	check_env

	echo "Downloading ${USER}/${PROG} ${RELEASE} (${URL})..."

	# Download and extract the binary to the temporary directory.
	pushd $TMP_DIR &> /dev/null

	local get_opts=("${INSECURE:+--insecure}" "--fail" "-#" "-L")

	case "${FTYPE}" in
		".gz")
			curl "${get_opts[@]}" "${URL}" | gzip -d - > "${PROG}" || fail "download and extraction failed"
			;;
		".tar.gz")
			curl "${get_opts[@]}" "${URL}" | tar xzf - > "${PROG}" || fail "download and extraction failed"
			;;
		".zip")
			local tmp_file=$(basename $URL)
		  curl "${get_opts[@]}" "${URL}" > "${tmp_file}" && unzip -o -qq "${tmp_file}" || fail "download and extraction failed"
			rm "${tmp_file}"
			;;
		"")
		  curl "${get_opts[@]}" "${URL}" > "kots_${OS}_${ARCH}" || fail "download failed"
			;;
		*)
			fail "unknown file type ${FTYPE}"
	esac

	echo "Installing to ${OUT_DIR}"

	TMP_BIN=$(find . -type f | xargs du | sort -n | tail -n 1 | cut -f 2)
	if [ ! -f "${TMP_BIN}" ]; then
		fail "could not find downloaded binary"
	fi

	#ensure its larger than 2MB
	if [[ $(du -m "${TMP_BIN}" | cut -f1) -lt 2 ]]; then
		fail "resulting file is smaller than 2MB, not a go binary"
	fi

	popd &> /dev/null

	#move into PATH or cwd
	chmod +x "${TMP_DIR}/${TMP_BIN}" || fail "chmod +x failed"

	if [[ -z "${USE_SUDO+x}" ]]; then
		mv "${TMP_DIR}/${TMP_BIN}" "${OUT_DIR}/kubectl-${PROG}" &> /dev/null || fail "installing to ${OUT_DIR} failed"
	else
		sudo mv "${TMP_DIR}/${TMP_BIN}" "${OUT_DIR}/kubectl-${PROG}" &> /dev/null || fail "installing to ${OUT_DIR} failed"
	fi

	echo "Installed at $OUT_DIR/kubectl-$PROG"
}

install