//go:build !nvfm

package nvfm

import "errors"

var ErrNotBuilt = errors.New("nvfm: package built without nvfm support")

type (
	Return              int32
	Handle              struct{}
	AddressType         int32
	ConnectParams       struct{}
	PCIDevice           struct{}
	GPUInfo             struct{}
	PartitionInfo       struct{}
	FabricPartitionList struct{}
)

func Init() error                            { return ErrNotBuilt }
func Shutdown() error                        { return ErrNotBuilt }
func Connect(ConnectParams) (*Handle, error) { return nil, ErrNotBuilt }
func (h *Handle) Disconnect() error          { return ErrNotBuilt }
func (h *Handle) GetSupportedFabricPartitions() (*FabricPartitionList, error) {
	return nil, ErrNotBuilt
}
func (h *Handle) ActivateFabricPartition(uint32) error                     { return ErrNotBuilt }
func (h *Handle) ActivateFabricPartitionWithVFs(uint32, []PCIDevice) error { return ErrNotBuilt }
func (h *Handle) DeactivateFabricPartition(uint32) error                   { return ErrNotBuilt }

