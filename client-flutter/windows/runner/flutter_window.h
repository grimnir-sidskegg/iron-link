#ifndef RUNNER_FLUTTER_WINDOW_H_
#define RUNNER_FLUTTER_WINDOW_H_

#include <flutter/dart_project.h>
#include <flutter/flutter_view_controller.h>
#include <flutter/method_channel.h>
#include <flutter/standard_method_codec.h>

#include <windows.h>
#include <shellapi.h>

#include <memory>

#include "win32_window.h"

// Broadcast message used by a second launch to surface the running instance
// (single-instance behavior). Both the runner entrypoint and this window's
// message handler turn this string into the same system-wide message id via
// RegisterWindowMessage, so a HWND_BROADCAST from the second process reaches
// the first one (and is ignored by every other app's windows).
constexpr const wchar_t kIronLinkShowInstanceMessage[] =
    L"IRON_LINK_SHOW_EXISTING_INSTANCE";

// A window that hosts a Flutter view plus a hand-rolled Win32 system-tray icon,
// driven from Dart over the "iron_link/tray" method channel (no plugin —
// matches the Linux StatusNotifierItem backend; see lib/src/platform/tray/).
class FlutterWindow : public Win32Window {
 public:
  // Creates a new FlutterWindow hosting a Flutter view running |project|.
  explicit FlutterWindow(const flutter::DartProject& project);
  virtual ~FlutterWindow();

 protected:
  // Win32Window:
  bool OnCreate() override;
  void OnDestroy() override;
  LRESULT MessageHandler(HWND window, UINT const message, WPARAM const wparam,
                         LPARAM const lparam) noexcept override;

 private:
  // ---- tray helpers ----
  void RegisterTrayChannel();
  void AddTrayIcon();     // NIM_ADD (+ NIM_SETVERSION); no-op if already added
  void RemoveTrayIcon();  // NIM_DELETE
  void SetIconFromArgs(const flutter::EncodableMap& args);
  void SetTooltipFromArgs(const flutter::EncodableMap& args);
  HMENU BuildMenu(const flutter::EncodableList& items);
  void ShowContextMenu(int x, int y);
  HICON IconFromBgra(const uint8_t* bgra, int width, int height);
  void NotifyDartMenuItem(int id);
  void NotifyDartClick();

  // Single-instance: bring the window back from the tray / minimized state and
  // to the foreground, then let Dart re-sync window_manager (see MessageHandler).
  void SurfaceWindow();

  // Force the embedder to re-present its GPU surface. Used on the show-from-tray
  // path, where a bare SW_SHOW resizes nothing and so never asks the engine to
  // repaint — a surface left blank/stale while hidden would otherwise stay white
  // until the process restarts. A visual no-op when the surface is healthy.
  void RepaintSurface();

  // The project to run.
  flutter::DartProject project_;

  // The Flutter instance hosted by this window.
  std::unique_ptr<flutter::FlutterViewController> flutter_controller_;

  // The tray method channel + Win32 tray state.
  std::unique_ptr<flutter::MethodChannel<flutter::EncodableValue>> tray_channel_;
  NOTIFYICONDATAW nid_{};
  bool tray_added_ = false;
  HICON tray_icon_ = nullptr;     // owned; DestroyIcon on replace + teardown
  HMENU current_menu_ = nullptr;  // owned; DestroyMenu on rebuild + teardown
  UINT wm_taskbar_created_ = 0;
  UINT wm_show_instance_ = 0;     // RegisterWindowMessage(kIronLinkShowInstanceMessage)
};

#endif  // RUNNER_FLUTTER_WINDOW_H_
