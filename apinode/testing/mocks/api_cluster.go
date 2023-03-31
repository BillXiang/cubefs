// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/cubefs/cubefs/apinode/sdk (interfaces: ICluster)

// Package mocks is a generated GoMock package.
package mocks

import (
	context "context"
	reflect "reflect"

	sdk "github.com/cubefs/cubefs/apinode/sdk"
	gomock "github.com/golang/mock/gomock"
)

// MockICluster is a mock of ICluster interface.
type MockICluster struct {
	ctrl     *gomock.Controller
	recorder *MockIClusterMockRecorder
}

// MockIClusterMockRecorder is the mock recorder for MockICluster.
type MockIClusterMockRecorder struct {
	mock *MockICluster
}

// NewMockICluster creates a new mock instance.
func NewMockICluster(ctrl *gomock.Controller) *MockICluster {
	mock := &MockICluster{ctrl: ctrl}
	mock.recorder = &MockIClusterMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockICluster) EXPECT() *MockIClusterMockRecorder {
	return m.recorder
}

// Addr mocks base method.
func (m *MockICluster) Addr() string {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Addr")
	ret0, _ := ret[0].(string)
	return ret0
}

// Addr indicates an expected call of Addr.
func (mr *MockIClusterMockRecorder) Addr() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Addr", reflect.TypeOf((*MockICluster)(nil).Addr))
}

// GetVol mocks base method.
func (m *MockICluster) GetVol(arg0 string) sdk.IVolume {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetVol", arg0)
	ret0, _ := ret[0].(sdk.IVolume)
	return ret0
}

// GetVol indicates an expected call of GetVol.
func (mr *MockIClusterMockRecorder) GetVol(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetVol", reflect.TypeOf((*MockICluster)(nil).GetVol), arg0)
}

// Info mocks base method.
func (m *MockICluster) Info() *sdk.ClusterInfo {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Info")
	ret0, _ := ret[0].(*sdk.ClusterInfo)
	return ret0
}

// Info indicates an expected call of Info.
func (mr *MockIClusterMockRecorder) Info() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Info", reflect.TypeOf((*MockICluster)(nil).Info))
}

// ListVols mocks base method.
func (m *MockICluster) ListVols() []*sdk.VolInfo {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ListVols")
	ret0, _ := ret[0].([]*sdk.VolInfo)
	return ret0
}

// ListVols indicates an expected call of ListVols.
func (mr *MockIClusterMockRecorder) ListVols() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ListVols", reflect.TypeOf((*MockICluster)(nil).ListVols))
}

// UpdateAddr mocks base method.
func (m *MockICluster) UpdateAddr(arg0 context.Context, arg1 string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "UpdateAddr", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// UpdateAddr indicates an expected call of UpdateAddr.
func (mr *MockIClusterMockRecorder) UpdateAddr(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "UpdateAddr", reflect.TypeOf((*MockICluster)(nil).UpdateAddr), arg0, arg1)
}
