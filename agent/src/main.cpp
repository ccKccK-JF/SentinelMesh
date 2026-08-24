#include <chrono>
#include <filesystem>
#include <iostream>
#include <stdexcept>
#include <string>
#include <thread>

#include "sentinel/procfs.hpp"

namespace {

struct Options {
  std::filesystem::path proc_root{"/proc"};
  std::chrono::seconds interval{5};
  bool once{false};
};

Options ParseOptions(int argc, char** argv) {
  Options options;
  for (int i = 1; i < argc; ++i) {
    const std::string argument = argv[i];
    if (argument == "--once") {
      options.once = true;
    } else if (argument == "--proc-root" && i + 1 < argc) {
      options.proc_root = argv[++i];
    } else if (argument == "--interval" && i + 1 < argc) {
      options.interval = std::chrono::seconds(std::stoul(argv[++i]));
    } else {
      throw std::invalid_argument("unknown or incomplete argument: " + argument);
    }
  }
  return options;
}

}  // namespace

int main(int argc, char** argv) {
  try {
    const auto options = ParseOptions(argc, argv);
    sentinel::ProcfsCollector collector(options.proc_root);

    // CPU and rate metrics require a delta between two samples.
    collector.Collect();
    std::this_thread::sleep_for(std::chrono::milliseconds(250));

    do {
      std::cout << sentinel::ToJson(collector.Collect()) << std::endl;
      if (options.once) break;
      std::this_thread::sleep_for(options.interval);
    } while (true);
  } catch (const std::exception& error) {
    std::cerr << "sentinel-agent: " << error.what() << std::endl;
    return 1;
  }
  return 0;
}
