# rardecode (copied in)

`github.com/nwaples/rardecode/v2` **v2.4.6**, copied unchanged except for the patch below.
BSD 2-Clause, Copyright (c) 2015, Nicholas Waples: see `LICENSE`.

Why a copy: a RAR 2.9-4.x member can switch to the PPMd coder in any block of its packed data,
and the block itself says how large a model to allocate (up to 256 MB, held in about 1.4 times
as much memory). Upstream has no limit for that and no option to set one, so the only way to
read old-format rar packages from the network at all was to refuse every compressed one.

## Local patch

Lines touched are marked `local addition` or `local change`; `local_test.go` tests them.

1. A limit on the PPMd model: option `MaxPPMdMemory(mb int)` and error `ErrPPMdMemoryTooLarge`.
   A PPMd block that asks for a larger model fails before the model is allocated.
   - `volume.go`: `options.maxPPMdMB`, `MaxPPMdMemory`
   - `reader.go`: hands the limit to the `decodeReader`
   - `decode_reader.go`: `decodeReader.maxPPMdMB`
   - `decode29.go`: `readBlockHeader` takes the limit and passes it on
   - `decode29_ppm.go`: `ppm29Decoder.init` checks it; `ErrPPMdMemoryTooLarge`
2. `ppm_model.go`, `subAllocator.init`: a model of the same size as the last one is reused
   (`>=` for upstream's `>`). Upstream allocates it anew for every block that asks for one.
3. `filters.go`, `getV3Filter`: RAR 3 filter programs other than the standard filters are
   refused with `ErrCustomFilter`. Such a program runs on the RAR virtual machine, up to 25
   million instructions each time it is applied, with nothing to stop it in between; rar itself
   only writes the standard filters, which the library runs as Go code.

## Updating

Copy the new version's `*.go` and `LICENSE` over this directory, re-apply the edits above
(`rg "local (addition|change)"` in the old copy shows them) and keep `local_test.go`, then `go vet ./... && go test ./...` in
`sidecar/`. `../../subs/rar_test.go` covers the limit.
