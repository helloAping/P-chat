//go:build !windows

package main

type singleInstanceLock struct{}

// acquireSingleInstance 非 Windows 平台暂不做单实例锁。
// acquireSingleInstance is a no-op outside Windows for now.
func acquireSingleInstance() (*singleInstanceLock, bool, error) {
	return &singleInstanceLock{}, false, nil
}

func (l *singleInstanceLock) release() {}

func signalExistingInstance() bool { return false }
