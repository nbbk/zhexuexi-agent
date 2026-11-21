// Hand-written gRPC service registration for StatsService.
//
// protoc-gen-go-grpc 在本机装不上（v1.64.0 下包路径解析不到），
// 这份文件是手写的等价物，与生成代码行为一致：服务名、方法路径、
// 编解码方式都与 Xray 的 StatsService 相同。以后插件装得上了，
// 可以用 protoc 重新生成并替换本文件，接口不变。
package statspb

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StatsServiceServer 是服务端要实现的接口。
type StatsServiceServer interface {
	GetStats(context.Context, *GetStatsRequest) (*GetStatsResponse, error)
	QueryStats(context.Context, *QueryStatsRequest) (*QueryStatsResponse, error)
	mustEmbedUnimplementedStatsServiceServer()
}

// UnimplementedStatsServiceServer 是默认实现（未实现的方法返回 Unimplemented），
// 测试的 mock server 嵌它，只实现要用的方法。
type UnimplementedStatsServiceServer struct{}

func (UnimplementedStatsServiceServer) GetStats(context.Context, *GetStatsRequest) (*GetStatsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetStats not implemented")
}

func (UnimplementedStatsServiceServer) QueryStats(context.Context, *QueryStatsRequest) (*QueryStatsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method QueryStats not implemented")
}

func (UnimplementedStatsServiceServer) mustEmbedUnimplementedStatsServiceServer() {}

// RegisterStatsServiceServer 把实现注册到 grpc.Server 上。
func RegisterStatsServiceServer(s grpc.ServiceRegistrar, srv StatsServiceServer) {
	s.RegisterService(&StatsService_ServiceDesc, srv)
}

func _StatsService_GetStats_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetStatsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(StatsServiceServer).GetStats(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/xray.app.stats.command.StatsService/GetStats",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(StatsServiceServer).GetStats(ctx, req.(*GetStatsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _StatsService_QueryStats_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(QueryStatsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(StatsServiceServer).QueryStats(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/xray.app.stats.command.StatsService/QueryStats",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(StatsServiceServer).QueryStats(ctx, req.(*QueryStatsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// StatsService_ServiceDesc 是 StatsService 的服务描述。
var StatsService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "xray.app.stats.command.StatsService",
	HandlerType: (*StatsServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "GetStats",
			Handler:    _StatsService_GetStats_Handler,
		},
		{
			MethodName: "QueryStats",
			Handler:    _StatsService_QueryStats_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "stats.proto",
}
