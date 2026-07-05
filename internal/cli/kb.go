package cli

type KBManager struct{}

func NewKBManager() *KBManager { return &KBManager{} }

func (m *KBManager) Views() []KBView                      { return nil }
func (m *KBManager) AddPath(string) error                 { return nil }
func (m *KBManager) RemovePath(string) error              { return nil }
func (m *KBManager) ScanStats() (int, int, error)         { return 0, 0, nil }
func (m *KBManager) Scan(string) error                    { return nil }
