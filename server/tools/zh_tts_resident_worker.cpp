// Isolated Chinese TTS protocol v1 worker. Never install over the legacy worker.
#include <windows.h>
#include <algorithm>
#include <atomic>
#include <cmath>
#include <cstdint>
#include <cstring>
#include <iostream>
#include <string>
#include <thread>
#include <vector>
#include "sherpa-onnx/c-api/c-api.h"

static uint16_t u16(const uint8_t *p) { return uint16_t(p[0]) | uint16_t(p[1]) << 8; }
static uint32_t u32(const uint8_t *p) { return uint32_t(u16(p)) | uint32_t(u16(p + 2)) << 16; }
static uint64_t u64(const uint8_t *p) { return uint64_t(u32(p)) | uint64_t(u32(p + 4)) << 32; }
static void put16(std::vector<uint8_t> &b, uint16_t x) { b.push_back(x & 255); b.push_back(x >> 8); }
static void put32(std::vector<uint8_t> &b, uint32_t x) { put16(b, x & 65535); put16(b, x >> 16); }
static void put64(std::vector<uint8_t> &b, uint64_t x) { put32(b, x & 0xffffffff); put32(b, x >> 32); }
static bool readAll(HANDLE h, uint8_t *p, size_t n) {
  while (n) { DWORD got = 0; if (!ReadFile(h, p, DWORD(std::min<size_t>(n, 65536)), &got, nullptr) || !got) return false; p += got; n -= got; }
  return true;
}
static int readHeader(HANDLE h, uint8_t *p) {
  size_t n=0;
  while (n<20) { DWORD got=0; if (!ReadFile(h,p+n,DWORD(20-n),&got,nullptr)||!got) return n? -1:0; n+=got; }
  return 1;
}
static bool writeAll(HANDLE h, const uint8_t *p, size_t n) {
  while (n) { DWORD got = 0; if (!WriteFile(h, p, DWORD(std::min<size_t>(n, 65536)), &got, nullptr) || !got) return false; p += got; n -= got; }
  return true;
}
static bool frame(HANDLE out, uint8_t kind, uint64_t id, const std::vector<uint8_t> &body) {
  std::vector<uint8_t> h = {'E','W','T','S',1,kind,0,0}; put64(h,id); put32(h,uint32_t(body.size()));
  return writeAll(out,h.data(),h.size()) && writeAll(out,body.data(),body.size());
}
static std::string utf8(const std::wstring &w) {
  int n=WideCharToMultiByte(CP_UTF8,WC_ERR_INVALID_CHARS,w.data(),int(w.size()),nullptr,0,nullptr,nullptr);
  if (!n) return {}; std::string s(n,'\0'); WideCharToMultiByte(CP_UTF8,WC_ERR_INVALID_CHARS,w.data(),int(w.size()),s.data(),n,nullptr,nullptr); return s;
}
static bool validUtf8(const std::string &s) {
  if (s.empty() || s.find('\0')!=std::string::npos) return false;
  int units=MultiByteToWideChar(CP_UTF8,MB_ERR_INVALID_CHARS,s.data(),int(s.size()),nullptr,0);
  return units>0 && units<=2000;
}
static std::vector<uint8_t> wav(const SherpaOnnxGeneratedAudio *a) {
  if (!a || a->n<=0 || a->sample_rate!=16000 || a->n>int((8*1024*1024-44)/2)) {
    std::cerr << "audio_invalid n=" << (a?a->n:0) << " rate=" << (a?a->sample_rate:0) << "\n";
    return {};
  }
  std::vector<uint8_t> b(44 + size_t(a->n)*2,0);
  auto p16=[&](size_t i,uint16_t x){b[i]=x&255;b[i+1]=x>>8;};
  auto p32=[&](size_t i,uint32_t x){p16(i,x&65535);p16(i+2,x>>16);};
  auto tag=[&](size_t i,const char *s){std::memcpy(b.data()+i,s,4);};
  tag(0,"RIFF");p32(4,uint32_t(b.size()-8));tag(8,"WAVE");tag(12,"fmt ");p32(16,16);
  p16(20,1);p16(22,1);p32(24,16000);p32(28,32000);p16(32,2);p16(34,16);
  tag(36,"data");p32(40,uint32_t(b.size()-44));
  for (int i=0;i<a->n;i++) { if (!std::isfinite(a->samples[i])) return {};
    float f=std::clamp(a->samples[i],-1.0f,1.0f);
    p16(44+size_t(i)*2,uint16_t(int16_t(std::lround(f*(f<0?32768:32767))))); }
  return b;
}
int wmain(int argc,wchar_t **argv) {
  if (argc!=4 || std::wstring(argv[1])!=L"--serve") return 2;
  wchar_t *end=nullptr; unsigned long slot=wcstoul(argv[2],&end,10);
  if (*end || slot>3) return 2;
  unsigned long parentId=wcstoul(argv[3],&end,10);
  if (*end || !parentId) return 2;
  HANDLE parent=OpenProcess(SYNCHRONIZE,FALSE,parentId);
  if (!parent || WaitForSingleObject(parent,0)!=WAIT_TIMEOUT) return 2;
  std::wstring mutexName=L"Local\\enwords-zh-tts-resident-v1-slot-"+std::to_wstring(slot);
  HANDLE mutex=CreateMutexW(nullptr,FALSE,mutexName.c_str());
  if (!mutex) return 3;
  const DWORD mutexWait=WaitForSingleObject(mutex,0);
  if (mutexWait!=WAIT_OBJECT_0 && mutexWait!=WAIT_ABANDONED) return 3;
  std::thread([parent] { WaitForSingleObject(parent,INFINITE); TerminateProcess(GetCurrentProcess(),91); }).detach();
  HMODULE dll=LoadLibraryExW(L"D:\\VL-models\\sherpa-onnx\\lib\\sherpa-onnx-c-api.dll",nullptr,
                             LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|LOAD_LIBRARY_SEARCH_DEFAULT_DIRS);
  if (!dll) return 4;
  auto create=reinterpret_cast<decltype(&SherpaOnnxCreateOfflineTts)>(GetProcAddress(dll,"SherpaOnnxCreateOfflineTts"));
  auto generate=reinterpret_cast<decltype(&SherpaOnnxOfflineTtsGenerateWithConfig)>(GetProcAddress(dll,"SherpaOnnxOfflineTtsGenerateWithConfig"));
  auto destroyAudio=reinterpret_cast<decltype(&SherpaOnnxDestroyOfflineTtsGeneratedAudio)>(GetProcAddress(dll,"SherpaOnnxDestroyOfflineTtsGeneratedAudio"));
  auto destroy=reinterpret_cast<decltype(&SherpaOnnxDestroyOfflineTts)>(GetProcAddress(dll,"SherpaOnnxDestroyOfflineTts"));
  if (!create||!generate||!destroyAudio||!destroy) return 4;
  const std::wstring root=L"D:\\VL-models\\models\\sherpa-onnx-vits-zh-ll\\";
  const auto model=utf8(root+L"model.onnx"),tokens=utf8(root+L"tokens.txt"),lexicon=utf8(root+L"lexicon.txt"),dict=utf8(root+L"dict");
  const auto rules=utf8(root+L"phone.fst")+","+utf8(root+L"date.fst")+","+utf8(root+L"number.fst");
  SherpaOnnxOfflineTtsConfig c{}; c.model.vits.model=model.c_str();c.model.vits.tokens=tokens.c_str();
  c.model.vits.lexicon=lexicon.c_str();c.model.vits.dict_dir=dict.c_str();c.model.vits.noise_scale=0.667f;
  c.model.vits.noise_scale_w=0.8f;c.model.vits.length_scale=1.0f;c.model.num_threads=1;c.model.provider="cpu";
  c.rule_fsts=rules.c_str();c.max_num_sentences=1;c.silence_scale=0.2f;
  const auto *tts=create(&c); if (!tts) return 5;
  SherpaOnnxGenerationConfig warm{};warm.sid=0;warm.speed=1.0f;warm.silence_scale=0.2f;
  auto *probe=generate(tts,"好",&warm,nullptr,nullptr);if (!probe) return 5;destroyAudio(probe);
  HANDLE in=GetStdHandle(STD_INPUT_HANDLE),out=GetStdHandle(STD_OUTPUT_HANDLE);
  if (!frame(out,2,0,{uint8_t(slot),1})) return 6;
  uint64_t last=0;
  while (true) {
    uint8_t h[20];int header=readHeader(in,h);if (!header) break; if (header<0) return 7;
    if (std::memcmp(h,"EWTS",4)||h[4]!=1||h[5]!=1||u16(h+6)!=0||u32(h+16)>8017||u64(h+8)<=last) return 7;
    uint64_t id=u64(h+8);last=id;uint32_t n=u32(h+16);if (n<5) return 7;
    std::vector<uint8_t> body(n);if (!readAll(in,body.data(),n)) return 7;
    uint8_t sid=body[0];uint16_t speed=u16(body.data()+1),len=u16(body.data()+3);
    if (sid>4||speed<600||speed>1800||len!=n-5||len>8000) {frame(out,4,id,{1,0});continue;}
    std::string s(reinterpret_cast<char*>(body.data()+5),len);
    if (!validUtf8(s)) {frame(out,4,id,{1,0});continue;}
    SherpaOnnxGenerationConfig g{};g.sid=sid;g.speed=float(speed)/1000.0f;g.silence_scale=0.2f;
    auto *a=generate(tts,s.c_str(),&g,nullptr,nullptr);auto b=wav(a);
    uint8_t failure=a?3:2;
    if (a) destroyAudio(a);
    if (!frame(out,b.empty()?4:3,id,b.empty()?std::vector<uint8_t>{failure,0}:b)) break;
  }
  destroy(tts);FreeLibrary(dll);ReleaseMutex(mutex);CloseHandle(mutex);CloseHandle(parent);
  return 0;
}
