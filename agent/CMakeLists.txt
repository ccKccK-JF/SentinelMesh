cmake_minimum_required(VERSION 3.20)
project(sentinel_agent LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

option(SENTINEL_BUILD_TESTS "Build agent unit tests" ON)

find_package(Protobuf REQUIRED)
find_package(gRPC CONFIG REQUIRED)
find_package(PkgConfig REQUIRED)
pkg_check_modules(LIBBPF REQUIRED IMPORTED_TARGET libbpf)
find_program(CLANG_EXECUTABLE NAMES clang REQUIRED)

get_filename_component(
    SENTINEL_PROTO_ROOT
    "${CMAKE_CURRENT_SOURCE_DIR}/../api/proto"
    ABSOLUTE
)
set(SENTINEL_PROTO_FILE "${SENTINEL_PROTO_ROOT}/sentinel/v1/telemetry.proto")
set(SENTINEL_GENERATED_DIR "${CMAKE_CURRENT_BINARY_DIR}/generated")
set(SENTINEL_PROTO_CC "${SENTINEL_GENERATED_DIR}/sentinel/v1/telemetry.pb.cc")
set(SENTINEL_PROTO_H "${SENTINEL_GENERATED_DIR}/sentinel/v1/telemetry.pb.h")
set(SENTINEL_GRPC_CC "${SENTINEL_GENERATED_DIR}/sentinel/v1/telemetry.grpc.pb.cc")
set(SENTINEL_GRPC_H "${SENTINEL_GENERATED_DIR}/sentinel/v1/telemetry.grpc.pb.h")

add_custom_command(
    OUTPUT
        "${SENTINEL_PROTO_CC}"
        "${SENTINEL_PROTO_H}"
        "${SENTINEL_GRPC_CC}"
        "${SENTINEL_GRPC_H}"
    COMMAND "${CMAKE_COMMAND}" -E make_directory "${SENTINEL_GENERATED_DIR}"
    COMMAND "${Protobuf_PROTOC_EXECUTABLE}"
        --proto_path=${SENTINEL_PROTO_ROOT}
        --cpp_out=${SENTINEL_GENERATED_DIR}
        --grpc_out=${SENTINEL_GENERATED_DIR}
        --plugin=protoc-gen-grpc=$<TARGET_FILE:gRPC::grpc_cpp_plugin>
        ${SENTINEL_PROTO_FILE}
    DEPENDS "${SENTINEL_PROTO_FILE}"
    VERBATIM
)

add_library(sentinel_proto
    "${SENTINEL_PROTO_CC}"
    "${SENTINEL_GRPC_CC}"
)
target_include_directories(sentinel_proto PUBLIC "${SENTINEL_GENERATED_DIR}")
target_link_libraries(sentinel_proto PUBLIC protobuf::libprotobuf gRPC::grpc++)

set(SENTINEL_BPF_OBJECT "${CMAKE_CURRENT_BINARY_DIR}/runqlat.bpf.o")
set(SENTINEL_BLOCKIO_BPF_OBJECT "${CMAKE_CURRENT_BINARY_DIR}/blocklat.bpf.o")
set(SENTINEL_TCP_BPF_OBJECT "${CMAKE_CURRENT_BINARY_DIR}/tcplat.bpf.o")
add_custom_command(
    OUTPUT "${SENTINEL_BPF_OBJECT}"
    COMMAND "${CLANG_EXECUTABLE}"
        -O2
        -g
        -target bpf
        -D__TARGET_ARCH_x86
        -I${CMAKE_CURRENT_SOURCE_DIR}/ebpf
        -I/usr/include/${CMAKE_LIBRARY_ARCHITECTURE}
        -c ${CMAKE_CURRENT_SOURCE_DIR}/ebpf/runqlat.bpf.c
        -o "${SENTINEL_BPF_OBJECT}"
    DEPENDS
        ${CMAKE_CURRENT_SOURCE_DIR}/ebpf/runqlat.bpf.c
        ${CMAKE_CURRENT_SOURCE_DIR}/ebpf/vmlinux_min.h
    VERBATIM
)
add_custom_command(
    OUTPUT "${SENTINEL_BLOCKIO_BPF_OBJECT}"
    COMMAND "${CLANG_EXECUTABLE}"
        -O2
        -g
        -target bpf
        -D__TARGET_ARCH_x86
        -I${CMAKE_CURRENT_SOURCE_DIR}/ebpf
        -I/usr/include/${CMAKE_LIBRARY_ARCHITECTURE}
        -c ${CMAKE_CURRENT_SOURCE_DIR}/ebpf/blocklat.bpf.c
        -o "${SENTINEL_BLOCKIO_BPF_OBJECT}"
    DEPENDS
        ${CMAKE_CURRENT_SOURCE_DIR}/ebpf/blocklat.bpf.c
        ${CMAKE_CURRENT_SOURCE_DIR}/ebpf/vmlinux_min.h
    VERBATIM
)
add_custom_command(
    OUTPUT "${SENTINEL_TCP_BPF_OBJECT}"
    COMMAND "${CLANG_EXECUTABLE}"
        -O2
        -g
        -target bpf
        -D__TARGET_ARCH_x86
        -I${CMAKE_CURRENT_SOURCE_DIR}/ebpf
        -I/usr/include/${CMAKE_LIBRARY_ARCHITECTURE}
        -c ${CMAKE_CURRENT_SOURCE_DIR}/ebpf/tcplat.bpf.c
        -o "${SENTINEL_TCP_BPF_OBJECT}"
    DEPENDS
        ${CMAKE_CURRENT_SOURCE_DIR}/ebpf/tcplat.bpf.c
        ${CMAKE_CURRENT_SOURCE_DIR}/ebpf/vmlinux_min.h
    VERBATIM
)
add_custom_target(
    sentinel_bpf_objects ALL
    DEPENDS
        "${SENTINEL_BPF_OBJECT}"
        "${SENTINEL_BLOCKIO_BPF_OBJECT}"
        "${SENTINEL_TCP_BPF_OBJECT}"
)

add_library(sentinel_procfs
    src/procfs.cpp
)
target_include_directories(sentinel_procfs PUBLIC include)
target_compile_options(sentinel_procfs PRIVATE -Wall -Wextra -Wpedantic)

add_library(sentinel_latency
    src/latency_histogram.cpp
)
target_include_directories(sentinel_latency PUBLIC include)
target_compile_options(sentinel_latency PRIVATE -Wall -Wextra -Wpedantic)

add_library(sentinel_telemetry
    src/telemetry_client.cpp
)
target_include_directories(sentinel_telemetry PUBLIC include)
target_compile_options(sentinel_telemetry PRIVATE -Wall -Wextra -Wpedantic)
target_link_libraries(sentinel_telemetry PUBLIC sentinel_proto sentinel_procfs)

add_library(sentinel_runqlat
    src/runqlat_probe.cpp
)
target_include_directories(sentinel_runqlat PUBLIC include)
target_compile_options(sentinel_runqlat PRIVATE -Wall -Wextra -Wpedantic)
target_link_libraries(sentinel_runqlat PUBLIC sentinel_latency PkgConfig::LIBBPF)

add_library(sentinel_blockio
    src/blockio_probe.cpp
)
target_include_directories(sentinel_blockio PUBLIC include)
target_compile_options(sentinel_blockio PRIVATE -Wall -Wextra -Wpedantic)
target_link_libraries(sentinel_blockio PUBLIC sentinel_latency PkgConfig::LIBBPF)

add_library(sentinel_tcp
    src/tcp_probe.cpp
)
target_include_directories(sentinel_tcp PUBLIC include)
target_compile_options(sentinel_tcp PRIVATE -Wall -Wextra -Wpedantic)
target_link_libraries(sentinel_tcp PUBLIC sentinel_latency PkgConfig::LIBBPF)

add_executable(sentinel-agent src/main.cpp)
target_link_libraries(
    sentinel-agent
    PRIVATE
        sentinel_procfs
        sentinel_telemetry
        sentinel_runqlat
        sentinel_blockio
        sentinel_tcp
)
add_dependencies(sentinel-agent sentinel_bpf_objects)
add_custom_command(
    TARGET sentinel-agent
    POST_BUILD
    COMMAND "${CMAKE_COMMAND}" -E copy_if_different
        "${SENTINEL_BPF_OBJECT}"
        $<TARGET_FILE_DIR:sentinel-agent>/runqlat.bpf.o
    COMMAND "${CMAKE_COMMAND}" -E copy_if_different
        "${SENTINEL_BLOCKIO_BPF_OBJECT}"
        $<TARGET_FILE_DIR:sentinel-agent>/blocklat.bpf.o
    COMMAND "${CMAKE_COMMAND}" -E copy_if_different
        "${SENTINEL_TCP_BPF_OBJECT}"
        $<TARGET_FILE_DIR:sentinel-agent>/tcplat.bpf.o
)

if(SENTINEL_BUILD_TESTS)
    enable_testing()
    add_executable(procfs_test tests/procfs_test.cpp)
    target_link_libraries(procfs_test PRIVATE sentinel_procfs)
    add_test(NAME procfs_test COMMAND procfs_test)

    add_executable(runqlat_probe_test tests/runqlat_probe_test.cpp)
    target_link_libraries(runqlat_probe_test PRIVATE sentinel_runqlat)
    add_test(NAME runqlat_probe_test COMMAND runqlat_probe_test)

    add_executable(blockio_probe_test tests/blockio_probe_test.cpp)
    target_link_libraries(blockio_probe_test PRIVATE sentinel_latency)
    add_test(NAME blockio_probe_test COMMAND blockio_probe_test)

    add_executable(tcp_probe_test tests/tcp_probe_test.cpp)
    target_link_libraries(tcp_probe_test PRIVATE sentinel_latency)
    add_test(NAME tcp_probe_test COMMAND tcp_probe_test)
endif()
