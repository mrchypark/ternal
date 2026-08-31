#!/bin/sh
set -eu

cd "$(CDPATH='' cd "$(dirname "$0")/.." && pwd)"

if git grep -n -E '\bTERNAL_[A-Z0-9_]*(RHIZA|RAUTHY|PIGEONS|IROH)[A-Z0-9_]*\b'; then
	echo 'Ternal-owned configuration must use role names, not dependency product names' >&2
	exit 1
fi

if git grep -n -E '\bRHIZA_[A-Z0-9_]+\b'; then
	echo 'storage implementation environment variables must not be public Ternal configuration' >&2
	exit 1
fi

echo 'Public configuration naming contract passed'
