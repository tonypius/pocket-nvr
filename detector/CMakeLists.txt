cmake_minimum_required(VERSION 3.16)
project(pocketnvr_detector CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

# Core logic library: portable, no platform or NCNN dependency.
add_library(nvr_core STATIC
  src/motion_gate.cpp
  src/zones.cpp
  src/event_state.cpp
  src/frame_queue.cpp
)
target_include_directories(nvr_core PUBLIC src)

find_package(ncnn QUIET)
option(NVR_WITH_NCNN "Build with NCNN inference backend" ${ncnn_FOUND})

if(NVR_WITH_NCNN)
  if(TARGET ncnn)
    set(NCNN_LINK ncnn)
  elseif(TARGET ncnn::ncnn)
    set(NCNN_LINK ncnn::ncnn)
  else()
    set(NCNN_LINK ${NCNN_LIBRARIES})
    include_directories(${NCNN_INCLUDE_DIRS})
  endif()
  message(STATUS "NCNN found: real inference backend enabled (${NCNN_LINK})")
  add_library(nvr_ncnn src/backend_ncnn.cpp)
  target_link_libraries(nvr_ncnn PUBLIC nvr_core ${NCNN_LINK})
  target_compile_definitions(nvr_ncnn PUBLIC NVR_WITH_NCNN=1)

  # Phase 0 B smoke: model + backend on a real frame (Mac CPU and phone).
  add_executable(vulkan_smoke tools/vulkan_smoke.cpp)
  target_link_libraries(vulkan_smoke PRIVATE nvr_ncnn)

  # The detector daemon (COMP-3).
  add_executable(nvrdet
    src/main.cpp
    src/capture.cpp
    src/dconfig.cpp
    src/governor.cpp
    src/http_post.cpp
    src/snapshot.cpp
  )
  target_include_directories(nvrdet PRIVATE third_party third_party/nlohmann third_party/stb)
  target_link_libraries(nvrdet PRIVATE nvr_core nvr_ncnn)
endif()

# Unit tests (dependency-free harness in tests/test_core.cpp).
enable_testing()
add_executable(core_tests tests/test_core.cpp)
target_link_libraries(core_tests PRIVATE nvr_core)
add_test(NAME core COMMAND core_tests)
