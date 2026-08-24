cmake_minimum_required(VERSION 3.20)
project(sentinel_agent LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

option(SENTINEL_BUILD_TESTS "Build agent unit tests" ON)

find_package(Protobuf REQUIRED)
find_package(gRPC CONFIG REQUIRED)

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

add_library(sentinel_procfs
    src/procfs.cpp
)
target_include_directories(sentinel_procfs PUBLIC include)
target_compile_options(sentinel_procfs PRIVATE -Wall -Wextra -Wpedantic)

add_library(sentinel_telemetry
    src/telemetry_client.cpp
)
target_include_directories(sentinel_telemetry PUBLIC include)
target_compile_options(sentinel_telemetry PRIVATE -Wall -Wextra -Wpedantic)
target_link_libraries(sentinel_telemetry PUBLIC sentinel_proto sentinel_procfs)

add_executable(sentinel-agent src/main.cpp)
target_link_libraries(sentinel-agent PRIVATE sentinel_procfs sentinel_telemetry)

if(SENTINEL_BUILD_TESTS)
    enable_testing()
    add_executable(procfs_test tests/procfs_test.cpp)
    target_link_libraries(procfs_test PRIVATE sentinel_procfs)
    add_test(NAME procfs_test COMMAND procfs_test)
endif()
