//go:build windows

package credstore

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

// dataBlob 对应 Windows DATA_BLOB 结构。
type dataBlob struct {
	cbData uint32
	pbData *byte
}

// CRYPTPROTECT_UI_FORBIDDEN：禁止任何 UI 提示（服务场景必须设置，避免弹窗卡死）。
const cryptProtectUIfForbidden = 0x1

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")

	kernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLocalFree = kernel32.NewProc("LocalFree")
)

// newPlatformStore 创建 Windows DPAPI 凭据存储。
func newPlatformStore(dataDir string) (Store, error) {
	return &fileStore{
		path:    filepath.Join(dataDir, "credentials.bin"),
		encrypt: dpapiProtect,
		decrypt: dpapiUnprotect,
	}, nil
}

// dpapiProtect 用 CryptProtectData 加密，结果绑定当前 Windows 用户（无熵、无 UI、非 LOCAL_MACHINE）。
func dpapiProtect(plain []byte) ([]byte, error) {
	var in, out dataBlob
	in.cbData = uint32(len(plain))
	if len(plain) > 0 {
		in.pbData = &plain[0]
	}
	// CryptProtectData(pDataIn, szDataDescr, pOptionalEntropy, pReserved, pPromptStruct, dwFlags, pDataOut)
	r1, _, err := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // 数据描述（可选，置空）
		0, // 可选熵（置空，使用用户主密钥）
		0, // 保留
		0, // 提示结构（置空）
		uintptr(cryptProtectUIfForbidden),
		uintptr(unsafe.Pointer(&out)),
	)
	if r1 == 0 {
		return nil, fmt.Errorf("[credstore] CryptProtectData 失败: %v", err)
	}
	// DPAPI 通过 LocalAlloc 分配输出，须用 LocalFree 释放
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	blob := make([]byte, int(out.cbData))
	copy(blob, unsafe.Slice(out.pbData, int(out.cbData)))
	return blob, nil
}

// dpapiUnprotect 用 CryptUnprotectData 解密。
func dpapiUnprotect(blob []byte) ([]byte, error) {
	var in, out dataBlob
	in.cbData = uint32(len(blob))
	if len(blob) > 0 {
		in.pbData = &blob[0]
	}
	r1, _, err := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // 数据描述输出（可选，置空）
		0, // 可选熵
		0, // 保留
		0, // 提示结构
		uintptr(cryptProtectUIfForbidden),
		uintptr(unsafe.Pointer(&out)),
	)
	if r1 == 0 {
		return nil, fmt.Errorf("[credstore] CryptUnprotectData 失败: %v", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	plain := make([]byte, int(out.cbData))
	copy(plain, unsafe.Slice(out.pbData, int(out.cbData)))
	return plain, nil
}
