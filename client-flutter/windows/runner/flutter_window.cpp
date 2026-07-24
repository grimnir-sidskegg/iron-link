#include "flutter_window.h"

#include <windowsx.h>  // GET_X_LPARAM / GET_Y_LPARAM

#include <optional>
#include <string>
#include <vector>

#include "flutter/generated_plugin_registrant.h"

namespace {

// Private window message for the tray icon's callbacks, and a stable icon id
// (reused across NIM_ADD/MODIFY/DELETE and the TaskbarCreated re-add).
constexpr UINT WMAPP_NOTIFYCALLBACK = WM_APP + 1;
constexpr UINT kTrayIconId = 1;

std::wstring Utf16FromUtf8(const std::string& s) {
  if (s.empty()) return std::wstring();
  int len = MultiByteToWideChar(CP_UTF8, 0, s.c_str(), static_cast<int>(s.size()),
                                nullptr, 0);
  std::wstring w(len, L'\0');
  MultiByteToWideChar(CP_UTF8, 0, s.c_str(), static_cast<int>(s.size()), w.data(),
                      len);
  return w;
}

const flutter::EncodableValue* Find(const flutter::EncodableMap& m,
                                    const char* key) {
  auto it = m.find(flutter::EncodableValue(std::string(key)));
  return it == m.end() ? nullptr : &it->second;
}

std::string GetString(const flutter::EncodableMap& m, const char* key,
                      const std::string& def) {
  if (const auto* v = Find(m, key)) {
    if (const auto* s = std::get_if<std::string>(v)) return *s;
  }
  return def;
}

bool GetBool(const flutter::EncodableMap& m, const char* key, bool def) {
  if (const auto* v = Find(m, key)) {
    if (const auto* b = std::get_if<bool>(v)) return *b;
  }
  return def;
}

int GetInt(const flutter::EncodableMap& m, const char* key, int def) {
  if (const auto* v = Find(m, key)) {
    if (const auto* i = std::get_if<int32_t>(v)) return *i;
    if (const auto* i = std::get_if<int64_t>(v)) return static_cast<int>(*i);
  }
  return def;
}

}  // namespace

FlutterWindow::FlutterWindow(const flutter::DartProject& project)
    : project_(project) {}

FlutterWindow::~FlutterWindow() {}

bool FlutterWindow::OnCreate() {
  if (!Win32Window::OnCreate()) {
    return false;
  }

  RECT frame = GetClientArea();

  // The size here must match the window dimensions to avoid unnecessary surface
  // creation / destruction in the startup path.
  flutter_controller_ = std::make_unique<flutter::FlutterViewController>(
      frame.right - frame.left, frame.bottom - frame.top, project_);
  // Ensure that basic setup of the controller was successful.
  if (!flutter_controller_->engine() || !flutter_controller_->view()) {
    return false;
  }
  RegisterPlugins(flutter_controller_->engine());
  SetChildContent(flutter_controller_->view()->GetNativeWindow());

  flutter_controller_->engine()->SetNextFrameCallback([&]() {
    this->Show();
  });

  // Flutter can complete the first frame before the "show window" callback is
  // registered. The following call ensures a frame is pending to ensure the
  // window is shown. It is a no-op if the first frame hasn't completed yet.
  flutter_controller_->ForceRedraw();

  // System tray: register the channel and prime the NOTIFYICONDATA. The icon
  // itself is added once Dart pushes an icon + calls "show".
  RegisterTrayChannel();
  wm_taskbar_created_ = RegisterWindowMessageW(L"TaskbarCreated");
  // Single-instance: the id a second launch broadcasts to ask us to surface.
  wm_show_instance_ = RegisterWindowMessageW(kIronLinkShowInstanceMessage);
  ZeroMemory(&nid_, sizeof(nid_));
  nid_.cbSize = sizeof(NOTIFYICONDATAW);
  nid_.hWnd = GetHandle();
  nid_.uID = kTrayIconId;
  nid_.uCallbackMessage = WMAPP_NOTIFYCALLBACK;
  nid_.uFlags = NIF_ICON | NIF_MESSAGE | NIF_TIP;
  wcscpy_s(nid_.szTip, ARRAYSIZE(nid_.szTip), L"iron-link");

  return true;
}

void FlutterWindow::OnDestroy() {
  // Drop the tray icon before the controller / handle go away.
  RemoveTrayIcon();
  if (tray_icon_) {
    DestroyIcon(tray_icon_);
    tray_icon_ = nullptr;
  }
  if (current_menu_) {
    DestroyMenu(current_menu_);
    current_menu_ = nullptr;
  }
  tray_channel_ = nullptr;

  if (flutter_controller_) {
    flutter_controller_ = nullptr;
  }

  Win32Window::OnDestroy();
}

LRESULT
FlutterWindow::MessageHandler(HWND hwnd, UINT const message,
                              WPARAM const wparam,
                              LPARAM const lparam) noexcept {
  // Give Flutter, including plugins, an opportunity to handle window messages.
  if (flutter_controller_) {
    std::optional<LRESULT> result =
        flutter_controller_->HandleTopLevelWindowProc(hwnd, message, wparam,
                                                      lparam);
    if (result) {
      return *result;
    }
  }

  // Explorer restarted → our icon is gone; re-add it.
  if (message == wm_taskbar_created_ && tray_added_) {
    tray_added_ = false;
    AddTrayIcon();
    return 0;
  }

  // A second launch asked us to come forward instead of starting its own copy.
  if (message == wm_show_instance_ && wm_show_instance_ != 0) {
    SurfaceWindow();
    return 0;
  }

  switch (message) {
    case WMAPP_NOTIFYCALLBACK:
      // NOTIFYICON_VERSION_4: event in LOWORD(lparam), cursor in wparam.
      switch (LOWORD(lparam)) {
        case NIN_SELECT:
        case WM_LBUTTONUP:
          NotifyDartClick();
          break;
        case WM_CONTEXTMENU:
        case WM_RBUTTONUP:
          ShowContextMenu(GET_X_LPARAM(wparam), GET_Y_LPARAM(wparam));
          break;
      }
      return 0;
    case WM_ACTIVATE:
      // Regaining focus asks the engine for one more frame. Cheap (a no-op if a
      // frame is already pending) and safe — it helps a merely-idle surface
      // repaint on refocus without perturbing the base's SetFocus handling,
      // which still runs via the fall-through below.
      if (LOWORD(wparam) != WA_INACTIVE && flutter_controller_) {
        flutter_controller_->ForceRedraw();
      }
      break;
    case WM_FONTCHANGE:
      flutter_controller_->engine()->ReloadSystemFonts();
      break;
  }

  return Win32Window::MessageHandler(hwnd, message, wparam, lparam);
}

void FlutterWindow::RegisterTrayChannel() {
  tray_channel_ =
      std::make_unique<flutter::MethodChannel<flutter::EncodableValue>>(
          flutter_controller_->engine()->messenger(), "iron_link/tray",
          &flutter::StandardMethodCodec::GetInstance());

  tray_channel_->SetMethodCallHandler(
      [this](const flutter::MethodCall<flutter::EncodableValue>& call,
             std::unique_ptr<flutter::MethodResult<flutter::EncodableValue>>
                 result) {
        const std::string& method = call.method_name();
        const auto* args =
            std::get_if<flutter::EncodableMap>(call.arguments());
        if (method == "setIcon") {
          if (args) SetIconFromArgs(*args);
          result->Success();
        } else if (method == "setMenu") {
          if (args) {
            if (const auto* items = Find(*args, "items")) {
              if (const auto* list =
                      std::get_if<flutter::EncodableList>(items)) {
                if (current_menu_) DestroyMenu(current_menu_);
                current_menu_ = BuildMenu(*list);
              }
            }
          }
          result->Success();
        } else if (method == "setTooltip") {
          if (args) SetTooltipFromArgs(*args);
          result->Success();
        } else if (method == "show") {
          AddTrayIcon();
          result->Success();
        } else if (method == "hide") {
          RemoveTrayIcon();
          result->Success();
        } else if (method == "repaint") {
          // Dart's show-from-tray path (WindowsTray.showWindow) calls this right
          // after window_manager.show() to force a fresh present — see below.
          RepaintSurface();
          result->Success();
        } else {
          result->NotImplemented();
        }
      });
}

void FlutterWindow::AddTrayIcon() {
  if (tray_added_ || !nid_.hIcon) return;  // need an icon before NIM_ADD
  Shell_NotifyIconW(NIM_ADD, &nid_);
  nid_.uVersion = NOTIFYICON_VERSION_4;  // not persisted — re-set on every ADD
  Shell_NotifyIconW(NIM_SETVERSION, &nid_);
  tray_added_ = true;
}

void FlutterWindow::RemoveTrayIcon() {
  if (tray_added_) {
    Shell_NotifyIconW(NIM_DELETE, &nid_);
    tray_added_ = false;
  }
}

void FlutterWindow::SetIconFromArgs(const flutter::EncodableMap& args) {
  const auto* v = Find(args, "pixels");
  if (!v) return;
  const auto* bytes = std::get_if<std::vector<uint8_t>>(v);
  if (!bytes) return;
  int w = GetInt(args, "width", 0);
  int h = GetInt(args, "height", 0);
  if (w <= 0 || h <= 0 ||
      static_cast<int>(bytes->size()) < w * h * 4) {
    return;
  }
  HICON icon = IconFromBgra(bytes->data(), w, h);
  if (!icon) return;
  if (tray_icon_) DestroyIcon(tray_icon_);
  tray_icon_ = icon;
  nid_.hIcon = icon;
  if (tray_added_) {
    Shell_NotifyIconW(NIM_MODIFY, &nid_);
  } else {
    AddTrayIcon();  // first icon — show it now
  }
}

void FlutterWindow::SetTooltipFromArgs(const flutter::EncodableMap& args) {
  std::wstring t = Utf16FromUtf8(GetString(args, "text", "iron-link"));
  wcsncpy_s(nid_.szTip, ARRAYSIZE(nid_.szTip), t.c_str(), _TRUNCATE);
  if (tray_added_) Shell_NotifyIconW(NIM_MODIFY, &nid_);
}

// Builds an HICON from straight (non-premultiplied) BGRA, top-down, w*h*4 bytes.
HICON FlutterWindow::IconFromBgra(const uint8_t* bgra, int width, int height) {
  BITMAPINFO bi = {};
  bi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
  bi.bmiHeader.biWidth = width;
  bi.bmiHeader.biHeight = -height;  // negative => top-down
  bi.bmiHeader.biPlanes = 1;
  bi.bmiHeader.biBitCount = 32;
  bi.bmiHeader.biCompression = BI_RGB;

  void* bits = nullptr;
  HBITMAP color =
      CreateDIBSection(nullptr, &bi, DIB_RGB_COLORS, &bits, nullptr, 0);
  if (!color || !bits) {
    if (color) DeleteObject(color);
    return nullptr;
  }
  memcpy(bits, bgra, static_cast<size_t>(width) * height * 4);

  // The AND mask is required; with a real alpha channel an all-zero mask
  // ("draw everywhere, alpha decides") is the standard trick.
  HBITMAP mask = CreateBitmap(width, height, 1, 1, nullptr);

  ICONINFO ii = {};
  ii.fIcon = TRUE;
  ii.hbmColor = color;
  ii.hbmMask = mask;
  HICON icon = CreateIconIndirect(&ii);  // system copies the bitmaps

  DeleteObject(color);
  DeleteObject(mask);
  return icon;
}

HMENU FlutterWindow::BuildMenu(const flutter::EncodableList& items) {
  HMENU menu = CreatePopupMenu();
  for (const auto& v : items) {
    const auto* m = std::get_if<flutter::EncodableMap>(&v);
    if (!m) continue;

    if (GetString(*m, "type", "normal") == "separator") {
      MENUITEMINFOW mii = {};
      mii.cbSize = sizeof(mii);
      mii.fMask = MIIM_FTYPE;
      mii.fType = MFT_SEPARATOR;
      InsertMenuItemW(menu, GetMenuItemCount(menu), TRUE, &mii);
      continue;
    }

    std::wstring label = Utf16FromUtf8(GetString(*m, "label", ""));
    MENUITEMINFOW mii = {};
    mii.cbSize = sizeof(mii);
    mii.fMask = MIIM_STRING | MIIM_ID | MIIM_STATE | MIIM_FTYPE;
    mii.fType = MFT_STRING;
    mii.dwTypeData = label.data();
    mii.wID = static_cast<UINT>(GetInt(*m, "id", 0));
    mii.fState = GetBool(*m, "enabled", true) ? MFS_ENABLED : MFS_GRAYED;

    if (const auto* sub = Find(*m, "submenu")) {
      if (const auto* list = std::get_if<flutter::EncodableList>(sub)) {
        mii.fMask |= MIIM_SUBMENU;
        mii.hSubMenu = BuildMenu(*list);  // recurse
      }
    }
    InsertMenuItemW(menu, GetMenuItemCount(menu), TRUE, &mii);
  }
  return menu;
}

void FlutterWindow::ShowContextMenu(int x, int y) {
  if (!current_menu_) return;
  HWND hwnd = GetHandle();
  // Required for a tray context menu to dismiss on outside-click (per MSDN).
  SetForegroundWindow(hwnd);
  UINT align =
      GetSystemMetrics(SM_MENUDROPALIGNMENT) ? TPM_RIGHTALIGN : TPM_LEFTALIGN;
  BOOL cmd = TrackPopupMenu(
      current_menu_,
      align | TPM_BOTTOMALIGN | TPM_RIGHTBUTTON | TPM_RETURNCMD | TPM_NONOTIFY,
      x, y, 0, hwnd, nullptr);
  PostMessage(hwnd, WM_NULL, 0, 0);  // benign task switch, also per MSDN
  if (cmd != 0) NotifyDartMenuItem(static_cast<int>(cmd));
}

void FlutterWindow::NotifyDartMenuItem(int id) {
  if (!tray_channel_) return;
  tray_channel_->InvokeMethod(
      "onMenuItem",
      std::make_unique<flutter::EncodableValue>(flutter::EncodableMap{
          {flutter::EncodableValue("id"), flutter::EncodableValue(id)}}));
}

void FlutterWindow::NotifyDartClick() {
  if (!tray_channel_) return;
  tray_channel_->InvokeMethod("onTrayClick",
                              std::make_unique<flutter::EncodableValue>());
}

void FlutterWindow::SurfaceWindow() {
  HWND hwnd = GetHandle();
  if (!hwnd) return;
  // Restore from minimized, or unhide from the tray (window_manager.hide() does
  // SW_HIDE), then pull to the front. The second instance handed us its
  // foreground right via AllowSetForegroundWindow, so this actually focuses
  // rather than just flashing the taskbar.
  ShowWindow(hwnd, IsIconic(hwnd) ? SW_RESTORE : SW_SHOW);
  SetForegroundWindow(hwnd);
  // A bare SW_SHOW after a hide-to-tray resizes nothing, so the engine is never
  // asked to repaint — force a fresh present so the window can't come back blank.
  RepaintSurface();
  // Mirror it through Dart so window_manager's view of visibility/focus stays
  // consistent with the native state and its own show/focus path runs too.
  if (tray_channel_) {
    tray_channel_->InvokeMethod("onShowRequested",
                                std::make_unique<flutter::EncodableValue>());
  }
}

void FlutterWindow::RepaintSurface() {
  if (!flutter_controller_) return;
  HWND hwnd = GetHandle();
  // The size nudge below needs a visible, non-minimized window; when we can't do
  // it, still ask the engine for a fresh frame.
  if (!hwnd || !IsWindowVisible(hwnd) || IsIconic(hwnd)) {
    flutter_controller_->ForceRedraw();
    return;
  }
  // Force the embedder to resize and re-present its ANGLE/D3D surface with a
  // ±1px SWP_FRAMECHANGED size toggle — the same trick window_manager uses in
  // ForceRefresh(). This drives WM_NCCALCSIZE + WM_SIZE, which repositions the
  // Flutter child view and makes the engine recreate/re-present its swap
  // surface. Cheap and a visual no-op when the surface is already healthy;
  // recovers a stale/blank surface left over from a hide-to-tray (SW_SHOW alone
  // triggers no resize) without forcing the user to restart the process.
  RECT rect;
  GetWindowRect(hwnd, &rect);
  const int w = rect.right - rect.left;
  const int h = rect.bottom - rect.top;
  const UINT flags = SWP_NOMOVE | SWP_NOZORDER | SWP_NOOWNERZORDER |
                     SWP_NOACTIVATE | SWP_FRAMECHANGED;
  SetWindowPos(hwnd, nullptr, 0, 0, w + 1, h, flags);
  SetWindowPos(hwnd, nullptr, 0, 0, w, h, flags);
  flutter_controller_->ForceRedraw();
}
