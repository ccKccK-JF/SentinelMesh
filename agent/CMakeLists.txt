cmake_minimum_required(VERSION 3.20)
project(sentinel_agent LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

option(SENTINEL_BUILD_TESTS "Build agent unit tests" ON)

add_library(sentinel_procfs
    src/procfs.cpp
)
target_include_directories(sentinel_procfs PUBLIC include)
target_compile_options(sentinel_procfs PRIVATE -Wall -Wextra -Wpedantic)

add_executable(sentinel-agent src/main.cpp)
target_link_libraries(sentinel-agent PRIVATE sentinel_procfs)

if(SENTINEL_BUILD_TESTS)
    enable_testing()
    add_executable(procfs_test tests/procfs_test.cpp)
    target_link_libraries(procfs_test PRIVATE sentinel_procfs)
    add_test(NAME procfs_test COMMAND procfs_test)
endif()
