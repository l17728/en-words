// Deterministic EWTS worker for isolated adapter fault tests. No model is loaded.
#include <windows.h>
#include <algorithm>
#include <cstdint>
#include <cstring>
#include <string>
#include <thread>
#include <vector>

static uint16_t u16(const unsigned char *p) { return p[0] | (uint16_t(p[1]) << 8); }
static uint32_t u32(const unsigned char *p) { return u16(p) | (uint32_t(u16(p + 2)) << 16); }
static uint64_t u64(const unsigned char *p) { return u32(p) | (uint64_t(u32(p + 4)) << 32); }
static void put16(std::vector<unsigned char>& b, uint16_t x) { b.push_back(x & 255); b.push_back(x >> 8); }
static void put32(std::vector<unsigned char>& b, uint32_t x) { put16(b, x & 65535); put16(b, x >> 16); }
static void put64(std::vector<unsigned char>& b, uint64_t x) { put32(b, x & 0xffffffff); put32(b, x >> 32); }
static bool readAll(HANDLE h, unsigned char *p, size_t n) {
  while (n) { DWORD got = 0; if (!ReadFile(h, p, DWORD(std::min<size_t>(n, 65536)), &got, nullptr) || !got) return false; p += got; n -= got; }
  return true;
}
static bool writeAll(HANDLE h, const std::vector<unsigned char>& b) {
  size_t i = 0; while (i < b.size()) { DWORD got = 0; if (!WriteFile(h, b.data() + i, DWORD(std::min<size_t>(b.size() - i, 65536)), &got, nullptr) || !got) return false; i += got; } return true;
}
static void frame(HANDLE out, unsigned char kind, uint64_t id, const std::vector<unsigned char>& body) {
  std::vector<unsigned char> b{'E','W','T','S',1,kind,0,0}; put64(b,id); put32(b,uint32_t(body.size())); b.insert(b.end(),body.begin(),body.end()); writeAll(out,b);
}
static std::vector<unsigned char> wav(unsigned char marker) {
  std::vector<unsigned char> b(44 + 16000 * 2,0); // one second of audible PCM16
  auto p16=[&](int i,uint16_t x){b[i]=x&255;b[i+1]=x>>8;};
  auto p32=[&](int i,uint32_t x){p16(i,x&65535);p16(i+2,x>>16);};
  std::memcpy(b.data(),"RIFF",4);p32(4,uint32_t(b.size()-8));std::memcpy(b.data()+8,"WAVEfmt ",8);p32(16,16);
  p16(20,1);p16(22,1);p32(24,16000);p32(28,32000);p16(32,2);p16(34,16);
  std::memcpy(b.data()+36,"data",4);p32(40,uint32_t(b.size()-44));
  for (size_t i=44;i<b.size();i+=2) p16(int(i),uint16_t(marker)*257);
  return b;
}
int wmain(int argc,wchar_t **argv) {
  if (argc!=4 || std::wstring(argv[1])!=L"--serve") return 2;
  unsigned long slot=wcstoul(argv[2],nullptr,10), parentId=wcstoul(argv[3],nullptr,10);
  HANDLE parent=OpenProcess(SYNCHRONIZE,FALSE,parentId); if (!parent) return 2;
  std::thread([parent] { WaitForSingleObject(parent,INFINITE); TerminateProcess(GetCurrentProcess(),91); }).detach();
  HANDLE in=GetStdHandle(STD_INPUT_HANDLE),out=GetStdHandle(STD_OUTPUT_HANDLE);
  frame(out,2,0,{static_cast<unsigned char>(slot),1});
  for (;;) {
    unsigned char h[20]; if (!readAll(in,h,20)) break;
    if (std::memcmp(h,"EWTS",4)||h[4]!=1||h[5]!=1||u32(h+16)>8017) return 7;
    uint32_t n=u32(h+16); std::vector<unsigned char> body(n);
    if (!readAll(in,body.data(),n)) break;
    std::string s(reinterpret_cast<char*>(body.data()+5),n-5); uint64_t id=u64(h+8);
    if (s=="CRASH") return 9;
    if (s=="SLOW_CRASH") { Sleep(700); return 9; }
    if (s=="HANG") { Sleep(30000); continue; }
    if (s=="SLOW_A") Sleep(700);
    if (s=="SLOW") Sleep(1800);
    if (s=="WRONG_ID") { frame(out,3,id+1,wav('X')); continue; }
    if (s=="BAD_WAV") { frame(out,3,id,{1,2,3}); continue; }
    if (s=="BAD_RATE") { auto b=wav('R'); b[24]=0; frame(out,3,id,b); continue; }
    if (s=="DUP_READY") { frame(out,2,0,{static_cast<unsigned char>(slot),1}); continue; }
    if (s=="OVERSIZE") { std::vector<unsigned char> b{'E','W','T','S',1,3,0,0};put64(b,id);put32(b,8*1024*1024+1);writeAll(out,b);continue; }
    frame(out,3,id,wav(s=="SLOW_A"?'A':s=="B"?'B':'Z'));
  }
  CloseHandle(parent); return 0;
}
