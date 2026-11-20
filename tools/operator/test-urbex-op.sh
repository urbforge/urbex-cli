#!/bin/bash
# Tests of urbex-op's choice of image (urbforge/urbex-cli#114), with fake
# podman and docker that log their calls: a published image is pulled when
# it is missing, one built here (podman's localhost/..., docker's bare name)
# never is. Run by 'mise run test'.
set -u
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fails=0

# A PATH with the tools urbex-op uses and one fake engine, so that the real
# podman or docker on this machine is never found.
tools=$tmp/tools
mkdir -p "$tools"
for t in bash sh sed id cat grep mkdir mv dirname rm; do
  ln -s "$(command -v "$t")" "$tools/$t"
done

# fake ENGINE PRESENT: an engine that logs its calls and has only PRESENT.
fake() {
  local dir=$tmp/$1-bin
  mkdir -p "$dir"
  cat >"$dir/$1" <<EOF
#!/bin/bash
echo "\$*" >>"$tmp/calls"
case "\$1 \$2" in
  "image inspect") [ "\$3" = "$2" ] || [ "\$5" = "$2" ] ;;
  *) exit 0 ;;
esac
EOF
  chmod +x "$dir/$1"
  echo "$dir"
}

# check NAME ENGINE IMAGE PRESENT WANT: run urbex-op with IMAGE; WANT is
# "pull" (it pulled IMAGE), "local" (it ran it, no pull) or "refuse".
check() {
  local name=$1 engine=$2 image=$3 present=$4 want=$5 bin rc got
  bin=$(fake "$engine" "$present")
  : >"$tmp/calls"
  mkdir -p "$tmp/ws/home"
  PATH=$bin:$tools URBEX_OPERATOR_IMAGE=$image HOME=$tmp \
    "$here/urbex-op" -w "$tmp/ws" true >"$tmp/out" 2>&1
  rc=$?
  if grep -q "^pull $image\$" "$tmp/calls"; then got=pull
  elif [ $rc -ne 0 ] && grep -q "build it with" "$tmp/out"; then got=refuse
  elif grep -q "^run .* $image " "$tmp/calls"; then got=local
  else got="unexpected (rc $rc)"
  fi
  if [ "$got" = "$want" ]; then
    echo "ok   $name"
  else
    echo "FAIL $name: $got, want $want"
    sed 's/^/     /' "$tmp/out" "$tmp/calls"
    fails=$((fails + 1))
  fi
}

published=ghcr.io/urbforge/urbex-op:0.1.0
check "podman: a published image, missing, is pulled" podman $published none pull
check "podman: a published image, present, is run" podman $published $published local
check "podman: localhost/..., missing, is never pulled" podman localhost/urbex-operator none refuse
check "podman: localhost:5000/..., missing, is never pulled" podman localhost:5000/urbex-op none refuse
check "podman: localhost/..., present, is run" podman localhost/urbex-operator-forgejo localhost/urbex-operator-forgejo local
check "docker: a bare name, missing, is never pulled" docker urbex-operator none refuse
check "docker: a bare name, present, is run" docker urbex-operator urbex-operator local
check "docker: a published image, missing, is pulled" docker $published none pull

# The default workspace: ~/urbex-work, or ~/urbex-workspace (urbex 0.1's
# default) when only that one exists (urbforge/urbex#57).
default_ws() { # default_ws NAME WANT [DIR...]: the workspace urbex-op picks with these dirs in HOME
  local name=$1 want=$2 home=$tmp/home-$RANDOM bin
  shift 2
  mkdir -p "$home"
  for d in "$@"; do mkdir -p "$home/$d/home"; done
  bin=$(fake podman localhost/urbex-operator)
  : >"$tmp/calls"
  PATH=$bin:$tools HOME=$home URBEX_OPERATOR_IMAGE=localhost/urbex-operator "$here/urbex-op" true >"$tmp/out" 2>&1
  if grep -q -- "-v $home/$want:/work" "$tmp/calls" || grep -q "no workspace at $home/$want" "$tmp/out"; then
    echo "ok   $name"
  else
    echo "FAIL $name: want ~/$want"; sed 's/^/     /' "$tmp/out" "$tmp/calls"; fails=$((fails + 1))
  fi
}
default_ws "default workspace: ~/urbex-work" urbex-work urbex-work
default_ws "default workspace: ~/urbex-workspace when only it exists" urbex-workspace urbex-workspace
default_ws "default workspace: ~/urbex-work when both exist" urbex-work urbex-work urbex-workspace
default_ws "default workspace: ~/urbex-work when none exists" urbex-work

[ $fails -eq 0 ] || { echo "$fails failed"; exit 1; }
