#!/bin/bash
set -e
export PATH="$HOME/sdk/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
cd /mnt/d/CoreX/Programming/Go/Axiom
"$@"
