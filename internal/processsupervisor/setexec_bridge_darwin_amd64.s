//go:build darwin && amd64

#include "textflag.h"

TEXT darwin_libc_posix_spawn_trampoline<>(SB),NOSPLIT,$0-0
	JMP	darwin_libc_posix_spawn(SB)
GLOBL	·darwinLibcPosixSpawnAddr(SB), RODATA, $8
DATA	·darwinLibcPosixSpawnAddr(SB)/8, $darwin_libc_posix_spawn_trampoline<>(SB)

TEXT darwin_libc_posix_spawnattr_init_trampoline<>(SB),NOSPLIT,$0-0
	JMP	darwin_libc_posix_spawnattr_init(SB)
GLOBL	·darwinLibcPosixSpawnAttrInitAddr(SB), RODATA, $8
DATA	·darwinLibcPosixSpawnAttrInitAddr(SB)/8, $darwin_libc_posix_spawnattr_init_trampoline<>(SB)

TEXT darwin_libc_posix_spawnattr_setflags_trampoline<>(SB),NOSPLIT,$0-0
	JMP	darwin_libc_posix_spawnattr_setflags(SB)
GLOBL	·darwinLibcPosixSpawnAttrSetFlagsAddr(SB), RODATA, $8
DATA	·darwinLibcPosixSpawnAttrSetFlagsAddr(SB)/8, $darwin_libc_posix_spawnattr_setflags_trampoline<>(SB)

TEXT darwin_libc_posix_spawnattr_destroy_trampoline<>(SB),NOSPLIT,$0-0
	JMP	darwin_libc_posix_spawnattr_destroy(SB)
GLOBL	·darwinLibcPosixSpawnAttrDestroyAddr(SB), RODATA, $8
DATA	·darwinLibcPosixSpawnAttrDestroyAddr(SB)/8, $darwin_libc_posix_spawnattr_destroy_trampoline<>(SB)
