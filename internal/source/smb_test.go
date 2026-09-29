package source

import "testing"

type fsInfo struct{ sector, perUnit, total, avail uint64 }

func (f fsInfo) BlockSize() uint64           { return f.sector }
func (f fsInfo) FragmentSize() uint64        { return f.perUnit }
func (f fsInfo) TotalBlockCount() uint64     { return f.total }
func (f fsInfo) FreeBlockCount() uint64      { return f.avail }
func (f fsInfo) AvailableBlockCount() uint64 { return f.avail }

func TestShareSpace(t *testing.T) {
	// Samba on the ASIAIR: 1 KiB units of two 512-byte sectors; the SD card
	// of 119.4 GB with 91.5 GB free showed as 59.7 GB with 45.7 GB free
	sp := shareSpace("TF Images", fsInfo{sector: 512, perUnit: 2, total: 116_601_000, avail: 89_355_000})
	if sp.Total != 116_601_000*1024 || sp.Free != 89_355_000*1024 {
		t.Fatalf("got %+v", sp)
	}
	// no sectors per unit reported: the sector is the unit
	if sp := shareSpace("x", fsInfo{sector: 4096, total: 10, avail: 5}); sp.Total != 40960 || sp.Free != 20480 {
		t.Fatalf("got %+v", sp)
	}
}
