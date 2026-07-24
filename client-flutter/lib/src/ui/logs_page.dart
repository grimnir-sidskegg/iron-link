import 'dart:io';

import 'package:file_selector/file_selector.dart';
import 'package:flutter/material.dart';

import '../app/session.dart';
import 'app.dart';
import 'theme/app_colors.dart';
import 'theme/app_typography.dart';
import 'widgets/iron_widgets.dart';

/// The merged session feed: sing-box log lines, core errors, subscription
/// updates. Lossy under load by design (the hub drops events for laggards)
/// — a debugging aid, not a ledger. A live text filter narrows the feed
/// (matches the message or the level, case-insensitive).
class LogsPage extends StatefulWidget {
  const LogsPage({super.key, required this.session});

  final DaemonSession session;

  @override
  State<LogsPage> createState() => _LogsPageState();
}

class _LogsPageState extends State<LogsPage> {
  final _searchController = TextEditingController();
  String _query = '';

  @override
  void dispose() {
    _searchController.dispose();
    super.dispose();
  }

  /// Saves the whole feed (oldest→newest, ignoring the live filter) to a text
  /// file the user picks. Each line is `<timestamp>  <LEVEL>  <message>`. A
  /// no-op if the feed is empty or the save dialog is cancelled.
  Future<void> _download() async {
    final lines = List<FeedLine>.of(widget.session.feed);
    if (lines.isEmpty) return;
    final location = await getSaveLocation(
      suggestedName: 'iron-link-logs-${_stamp(DateTime.now())}.txt',
      acceptedTypeGroups: const [
        XTypeGroup(label: 'Log / text', extensions: ['txt', 'log']),
      ],
    );
    if (location == null || !mounted) return; // cancelled
    final buffer = StringBuffer();
    for (final l in lines) {
      buffer.writeln('${_stampReadable(l.at)}  ${l.level.toUpperCase().padRight(5)}  ${l.message}');
    }
    try {
      await File(location.path).writeAsString(buffer.toString());
      if (mounted) showSnack(context, 'Saved ${lines.length} lines to ${location.path}');
    } on FileSystemException catch (e) {
      if (mounted) showSnack(context, 'Could not save: ${e.message}', error: true);
    }
  }

  /// `YYYYMMDD-HHMMSS` — a filename-safe stamp for the suggested name.
  String _stamp(DateTime d) {
    String p(int n) => n.toString().padLeft(2, '0');
    return '${d.year}${p(d.month)}${p(d.day)}-${p(d.hour)}${p(d.minute)}${p(d.second)}';
  }

  /// `YYYY-MM-DD HH:MM:SS` — a readable per-line timestamp for the file.
  String _stampReadable(DateTime d) {
    String p(int n) => n.toString().padLeft(2, '0');
    return '${d.year}-${p(d.month)}-${p(d.day)} ${p(d.hour)}:${p(d.minute)}:${p(d.second)}';
  }

  Color _levelColor(BuildContext context, String level) {
    final c = context.appColors;
    return switch (level.toLowerCase()) {
      'error' || 'fatal' => c.logError,
      'warn' || 'warning' => c.logWarn,
      'info' => c.logInfo,
      _ => c.logTrace,
    };
  }

  bool _matches(FeedLine line, String q) =>
      line.message.toLowerCase().contains(q) ||
      line.level.toLowerCase().contains(q);

  /// sing-box opens a line with `[<connId> <age>] …` (after we strip the level
  /// + uptime). Colour the numeric connection id (from the theme's
  /// connection palette) so a connection's lines are visually grouped;
  /// everything else renders in [dim].
  List<TextSpan> _logSpans(BuildContext context, String message) {
    final m = RegExp(r'^\[(\d+) ').firstMatch(message);
    if (m == null) return [TextSpan(text: message)];
    final id = m.group(1)!;
    return [
      const TextSpan(text: '['),
      TextSpan(
        text: id,
        style: TextStyle(
            color: context.appColors.connectionTint(id),
            fontWeight: FontWeight.w600),
      ),
      TextSpan(text: message.substring(1 + id.length)),
    ];
  }

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: widget.session,
      builder: (context, _) {
        final t = context.iron;
        final q = _query.toLowerCase();
        final hasLines = widget.session.feed.isNotEmpty;
        final lines = [
          for (final l in widget.session.feed)
            if (q.isEmpty || _matches(l, q)) l,
        ];
        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // ---- Title + subtitle + header actions (Download / Clear) ----
            Padding(
              padding: const EdgeInsets.fromLTRB(24, 16, 24, 4),
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text('Logs', style: context.screenTitle),
                        const SizedBox(height: 2),
                        Text('Live output from the core.',
                            style: Theme.of(context)
                                .textTheme
                                .bodyMedium
                                ?.copyWith(color: t.dim)),
                      ],
                    ),
                  ),
                  const SizedBox(width: 16),
                  IronButton(
                    label: 'Download',
                    icon: Icons.download_rounded,
                    onPressed: hasLines ? _download : null,
                  ),
                  const SizedBox(width: 8),
                  IronButton(
                    label: 'Clear',
                    icon: Icons.clear_all,
                    onPressed: hasLines
                        ? () {
                            widget.session.feed.clear();
                            // ignore: invalid_use_of_protected_member, invalid_use_of_visible_for_testing_member
                            widget.session.notifyListeners();
                          }
                        : null,
                  ),
                ],
              ),
            ),
            // ---- Filter ----
            Padding(
              padding: const EdgeInsets.fromLTRB(24, 8, 24, 10),
              child: SizedBox(
                height: 40,
                child: TextField(
                  controller: _searchController,
                  style: context.monoBodySmall,
                  decoration: InputDecoration(
                    isDense: true,
                    filled: true,
                    fillColor: t.raised,
                    prefixIcon: Icon(Icons.search, size: 18, color: t.faint),
                    hintText: 'Filter…',
                    hintStyle: TextStyle(color: t.faint),
                    enabledBorder: OutlineInputBorder(
                      borderRadius: BorderRadius.circular(11),
                      borderSide: BorderSide(color: t.border),
                    ),
                    focusedBorder: OutlineInputBorder(
                      borderRadius: BorderRadius.circular(11),
                      borderSide: BorderSide(color: t.accent),
                    ),
                    border: OutlineInputBorder(
                      borderRadius: BorderRadius.circular(11),
                      borderSide: BorderSide(color: t.border),
                    ),
                    suffixIcon: _query.isEmpty
                        ? null
                        : IconButton(
                            tooltip: 'Clear filter',
                            icon: Icon(Icons.close, size: 18, color: t.dim),
                            onPressed: () {
                              _searchController.clear();
                              setState(() => _query = '');
                            },
                          ),
                  ),
                  onChanged: (v) => setState(() => _query = v),
                ),
              ),
            ),
            // ---- The console: deepest surface, mono lines ----
            Expanded(
              child: Padding(
                padding: const EdgeInsets.fromLTRB(24, 0, 24, 88),
                child: IronCard(
                  color: t.sidebar,
                  radius: 14,
                  padding: const EdgeInsets.symmetric(
                      horizontal: 12, vertical: 8),
                  child: lines.isEmpty
                      ? Center(
                          child: Text(
                            _query.isEmpty
                                ? 'No events yet.'
                                : 'No lines match “$_query”.',
                            style: Theme.of(context)
                                .textTheme
                                .bodyMedium
                                ?.copyWith(color: t.dim),
                          ),
                        )
                      : ListView.builder(
                          reverse: true,
                          padding: const EdgeInsets.symmetric(vertical: 4),
                          itemCount: lines.length,
                          itemBuilder: (context, i) {
                            final line = lines[lines.length - 1 - i];
                            final time = TimeOfDay.fromDateTime(line.at)
                                .format(context);
                            return Padding(
                              padding:
                                  const EdgeInsets.symmetric(vertical: 1.5),
                              child: Row(
                                crossAxisAlignment:
                                    CrossAxisAlignment.start,
                                children: [
                                  Text(time,
                                      style: context.mono(12,
                                          color: t.faint)),
                                  const SizedBox(width: 10),
                                  SizedBox(
                                    width: 46,
                                    child: Text(line.level,
                                        style: context.mono(12,
                                            weight: FontWeight.w600,
                                            color: _levelColor(
                                                context, line.level))),
                                  ),
                                  const SizedBox(width: 6),
                                  Expanded(
                                    child: SelectableText.rich(
                                      TextSpan(
                                        style: context.mono(12, color: t.dim),
                                        children:
                                            _logSpans(context, line.message),
                                      ),
                                    ),
                                  ),
                                ],
                              ),
                            );
                          },
                        ),
                ),
              ),
            ),
          ],
        );
      },
    );
  }
}
