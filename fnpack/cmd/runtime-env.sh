#!/bin/bash

: "${TRIM_PKGVAR:?缺少应用私有目录}"
: "${TRIM_APPDEST:?缺少应用安装目录}"

case "${wizard_data_library_type:-data}" in
    data) DATA_LIBRARY_PATH="$TRIM_PKGVAR" ;;
    share)
        : "${TRIM_DATA_SHARE_PATHS:?缺少应用共享目录}"
        DATA_LIBRARY_PATH="${TRIM_DATA_SHARE_PATHS%%:*}" ;;
    *) printf '%s\n' 'OpenChamber 存储位置无效。' >&2; exit 1 ;;
esac
if [ -z "$DATA_LIBRARY_PATH" ] || [ ! -d "$DATA_LIBRARY_PATH" ]; then
    printf '%s\n' 'OpenChamber 数据目录不存在。' >&2
    exit 1
fi
DATA_LIBRARY_PATH=$(readlink -f -- "$DATA_LIBRARY_PATH")
if ! [[ "${wizard_data_library_type:-data}:$DATA_LIBRARY_PATH" =~ ^(data:/vol[0-9]+/@appdata/openchamber|share:/vol[0-9]+/@appshare/openchamber)$ ]]; then
    printf '%s\n' 'OpenChamber 数据目录路径无效。' >&2
    exit 1
fi

case "${wizard_openchamber_run_user:-root}" in
    root) OPENCHAMBER_RUN_USER=root ;;
    openchamber) OPENCHAMBER_RUN_USER=openchamber ;;
    *) printf '%s\n' 'OpenChamber 运行用户无效。' >&2; exit 1 ;;
esac
export DATA_LIBRARY_PATH OPENCHAMBER_RUN_USER
