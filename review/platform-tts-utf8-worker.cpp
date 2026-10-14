// Candidate platform helper. Build against the deployed sherpa-onnx headers.
// Install beside manager/tts-server.js; all native dependencies remain unchanged.
#include <windows.h>
#include <algorithm>
#include <cmath>
#include <cstdint>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <iterator>
#include <string>
#include <vector>
#include "sherpa-onnx/c-api/c-api.h"

static std::string utf8(const std::wstring &s) {
  const int n = WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, s.data(), static_cast<int>(s.size()), nullptr, 0, nullptr, nullptr);
  if (!n) return {};
  std::string out(n, '\0');
  WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, s.data(), static_cast<int>(s.size()), out.data(), n, nullptr, nullptr);
  return out;
}

int wmain(int argc, wchar_t **argv) {
  if (argc != 5) return 2;
  wchar_t *end = nullptr;
  const long sid = wcstol(argv[3], &end, 10);
  if (end == argv[3] || *end || sid < 0 || sid > 4) return 2;
  const float speed = wcstof(argv[4], &end);
  if (end == argv[4] || *end || !std::isfinite(speed) || speed < 0.6f || speed > 1.8f) return 2;
  std::error_code fileError;
  const auto inputSize = std::filesystem::file_size(std::filesystem::path(argv[1]), fileError);
  if (fileError || inputSize == 0 || inputSize > 8000) return 3;
  std::ifstream input(std::filesystem::path(argv[1]), std::ios::binary);
  std::string text((std::istreambuf_iterator<char>(input)), {});
  if (!input || text.empty() || text.size() > 8000 || text.find('\0') != std::string::npos ||
      !MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, text.data(), static_cast<int>(text.size()), nullptr, 0)) return 3;
  wchar_t module[32768];
  const DWORD size = GetModuleFileNameW(nullptr, module, 32768);
  if (!size || size == 32768) return 4;
  const auto root = std::filesystem::path(std::wstring(module, size)).parent_path().parent_path();
  const auto lib = root / L"sherpa-onnx" / L"lib" / L"sherpa-onnx-c-api.dll";
  const auto model = root / L"models" / L"sherpa-onnx-vits-zh-ll";
  HMODULE dll = LoadLibraryExW(lib.c_str(), nullptr, LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | LOAD_LIBRARY_SEARCH_DEFAULT_DIRS);
  if (!dll) return 4;
  auto create = reinterpret_cast<decltype(&SherpaOnnxCreateOfflineTts)>(GetProcAddress(dll, "SherpaOnnxCreateOfflineTts"));
  auto generate = reinterpret_cast<decltype(&SherpaOnnxOfflineTtsGenerateWithConfig)>(GetProcAddress(dll, "SherpaOnnxOfflineTtsGenerateWithConfig"));
  auto destroyAudio = reinterpret_cast<decltype(&SherpaOnnxDestroyOfflineTtsGeneratedAudio)>(GetProcAddress(dll, "SherpaOnnxDestroyOfflineTtsGeneratedAudio"));
  auto destroy = reinterpret_cast<decltype(&SherpaOnnxDestroyOfflineTts)>(GetProcAddress(dll, "SherpaOnnxDestroyOfflineTts"));
  if (!create || !generate || !destroyAudio || !destroy) return 5;
  const auto onnx = utf8((model / L"model.onnx").wstring());
  const auto tokens = utf8((model / L"tokens.txt").wstring());
  const auto lexicon = utf8((model / L"lexicon.txt").wstring());
  const auto dictionary = utf8((model / L"dict").wstring());
  const auto rules = utf8((model / L"phone.fst").wstring()) + "," + utf8((model / L"date.fst").wstring()) + "," + utf8((model / L"number.fst").wstring());
  SherpaOnnxOfflineTtsConfig config{};
  config.model.vits.model = onnx.c_str(); config.model.vits.tokens = tokens.c_str(); config.model.vits.lexicon = lexicon.c_str();
  config.model.vits.dict_dir = dictionary.c_str();
  config.model.vits.noise_scale = 0.667f; config.model.vits.noise_scale_w = 0.8f; config.model.vits.length_scale = 1.0f;
  config.model.num_threads = 4; config.model.provider = "cpu";
  config.rule_fsts = rules.c_str(); config.max_num_sentences = 1; config.silence_scale = 0.2f;
  const auto *tts = create(&config);
  if (!tts) return 6;
  SherpaOnnxGenerationConfig generation{};
  generation.sid = static_cast<int32_t>(sid); generation.speed = speed; generation.silence_scale = 0.2f;
  const auto *audio = generate(tts, text.c_str(), &generation, nullptr, nullptr);
  int result = 7;
  if (audio && audio->n > 0 && audio->sample_rate > 0 && audio->n < INT32_MAX / 2) {
    const uint32_t bytes = static_cast<uint32_t>(audio->n) * 2;
    std::vector<uint8_t> wav(44 + bytes, 0);
    auto u16 = [&](size_t i, uint16_t v) { wav[i] = v & 255; wav[i + 1] = v >> 8; };
    auto u32 = [&](size_t i, uint32_t v) { u16(i, v & 65535); u16(i + 2, v >> 16); };
    auto label = [&](size_t i, const char *v) { std::copy(v, v + 4, wav.begin() + i); };
    label(0, "RIFF"); u32(4, 36 + bytes); label(8, "WAVE"); label(12, "fmt "); u32(16, 16);
    u16(20, 1); u16(22, 1); u32(24, audio->sample_rate); u32(28, audio->sample_rate * 2);
    u16(32, 2); u16(34, 16); label(36, "data"); u32(40, bytes);
    bool valid = true;
    for (int i = 0; i < audio->n; ++i) {
      if (!std::isfinite(audio->samples[i])) { valid = false; break; }
      const float v = std::clamp(audio->samples[i], -1.0f, 1.0f);
      u16(44 + i * 2, static_cast<uint16_t>(static_cast<int16_t>(std::lround(v * (v < 0 ? 32768 : 32767)))));
    }
    if (valid) {
      HANDLE file = CreateFileW(argv[2], GENERIC_WRITE, 0, nullptr, CREATE_NEW, FILE_ATTRIBUTE_NORMAL, nullptr);
      if (file != INVALID_HANDLE_VALUE) {
        DWORD written = 0;
        if (WriteFile(file, wav.data(), static_cast<DWORD>(wav.size()), &written, nullptr) && written == wav.size()) result = 0;
        CloseHandle(file);
      }
    }
  }
  if (audio) destroyAudio(audio);
  destroy(tts); FreeLibrary(dll);
  if (result) std::cerr << "tts_utf8_helper_failed code=" << result << "\n";
  return result;
}
