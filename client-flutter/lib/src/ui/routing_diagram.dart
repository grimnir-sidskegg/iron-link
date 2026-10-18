import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import '../app/session.dart';
import '../model/routing_draft.dart';
import 'iron_scope.dart';
import 'routing_widgets.dart';
import 'theme/app_colors.dart';
import 'theme/app_typography.dart';
import 'widgets/iron_widgets.dart';
import 'widgets/motion.dart';

/// Height of one destination lane.
const double _laneHeight = 44;

/// Width of the fan-out (You → lanes) and fan-in (lanes → Internet) curves.
const double _fanWidth = 32;

/// Max width of a lane's destination (pill + auto chip), centred on its
/// thread.
const double _destWidth = 170;

/// The active routing at a glance: "You" fans out into one lane per
/// destination [routeLanes] derives from [draft] (null = no active routing:
/// a single DefaultProxy lane), each ending at its node / Direct → Internet,
/// or at a stop sign for Block. Tapping "You" lists every rule in order plus
/// the default; tapping a lane lists only the rules routing there. The
/// threads flow while the session runs (reduce-motion respected); the
/// topology is static.
class RoutingDiagram extends StatelessWidget {
  const RoutingDiagram({
    super.key,
    required this.session,
    required this.draft,
    required this.nodes,
  });

  final DaemonSession session;
  final RoutingDraft? draft;
  final List<NodeRef> nodes;

  @override
  Widget build(BuildContext context) {
    final lanes = routeLanes(draft, nodes);
    return IronCard(
      child: ListenableBuilder(
        listenable: session,
        builder: (context, _) {
          final running = session.isRunning;
          final animate = running && !IronScope.reduceMotionOf(context);
          final colors = [
            for (final l in lanes) routeTargetColor(context, l.target.kind),
          ];
          final ys = [
            for (var i = 0; i < lanes.length; i++) (i + 0.5) * _laneHeight,
          ];
          final exits = [
            for (var i = 0; i < lanes.length; i++)
              if (lanes[i].target.kind != 'Block') i,
          ];
          final height = lanes.length * _laneHeight;
          return Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                draft == null
                    ? 'No active routing: everything goes via the active node'
                    : 'Active routing: ${draft!.name}',
                overflow: TextOverflow.ellipsis,
                style: Theme.of(context).textTheme.titleSmall,
              ),
              const SizedBox(height: 12),
              Row(
                children: [
                  _menu(
                    context,
                    _rows(
                      context,
                      List.generate(_rules.length, (i) => i),
                      withDefault: true,
                    ),
                    MouseRegion(
                      cursor: SystemMouseCursors.click,
                      child: IronEndNode(
                        icon: Icons.laptop_mac,
                        label: 'You',
                        active: running,
                      ),
                    ),
                  ),
                  CustomPaint(
                    size: Size(_fanWidth, height),
                    painter: _FanPainter(ys: ys, colors: colors, out: true),
                  ),
                  Expanded(
                    child: Column(
                      children: [
                        for (var i = 0; i < lanes.length; i++)
                          _menu(
                            context,
                            _rows(
                              context,
                              lanes[i].rules,
                              withDefault: lanes[i].isDefault,
                            ),
                            _lane(
                              context,
                              lanes[i],
                              colors[i],
                              animate,
                              running,
                            ),
                          ),
                      ],
                    ),
                  ),
                  if (exits.isNotEmpty) ...[
                    CustomPaint(
                      size: Size(_fanWidth, height),
                      painter: _FanPainter(
                        ys: [for (final i in exits) ys[i]],
                        colors: [for (final i in exits) colors[i]],
                        out: false,
                      ),
                    ),
                    IronEndNode(
                      icon: Icons.public,
                      label: 'Internet',
                      active: running,
                    ),
                  ],
                ],
              ),
            ],
          );
        },
      ),
    );
  }

  List<RuleDraft> get _rules => draft?.rules ?? const [];

  TargetDraft get _defaultTarget =>
      draft?.defaultTarget ?? RouteTarget('DefaultProxy');

  /// One lane: the thread (with a "default" caption on the default target's
  /// lane), the destination pill mid-thread, then the rest of the thread into
  /// the fan-in — or into a stop sign for Block.
  Widget _lane(
    BuildContext context,
    RouteLane lane,
    Color color,
    bool animate,
    bool running,
  ) {
    final t = context.iron;
    final target = lane.target;
    final missing = target.kind == nodeToken && lane.node == null;
    final label = target.kind == 'DefaultProxy'
        ? (running ? session.activeNodeLive ?? session.active?.node : null) ??
              target.kind
        : routeTargetLabel(target, nodes);
    Widget thread() => PipelineThread(
      animate: animate,
      accent: color,
      idle: color.withValues(alpha: 0.5),
      height: 16,
    );
    return MouseRegion(
      cursor: SystemMouseCursors.click,
      child: SizedBox(
        height: _laneHeight,
        child: Row(
          children: [
            Expanded(
              child: Stack(
                alignment: Alignment.center,
                fit: StackFit.expand,
                children: [
                  thread(),
                  if (lane.isDefault)
                    Positioned(
                      top: 0,
                      child: Text(
                        'default',
                        style: context.mono(9.5, color: t.faint),
                      ),
                    ),
                ],
              ),
            ),
            ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: _destWidth),
              child: Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Flexible(
                    child: IronTag(label, color: missing ? t.faint : color),
                  ),
                  if (lane.node?.isGroup ?? false) ...[
                    const SizedBox(width: 6),
                    const IronKindChip('auto', accent: true),
                  ],
                ],
              ),
            ),
            Expanded(child: thread()),
            if (target.kind == 'Block')
              Icon(Icons.block, size: 18, color: color),
          ],
        ),
      ),
    );
  }

  /// The rules at [indices] (1-based numbers, summary, target pill), plus the
  /// "default → target" line when [withDefault].
  List<Widget> _rows(
    BuildContext context,
    List<int> indices, {
    required bool withDefault,
  }) {
    final t = context.iron;
    Widget row(Widget lead, Widget summary, Widget? pill) => SizedBox(
      width: 380,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
        child: Row(
          children: [
            SizedBox(width: 28, child: lead),
            Expanded(child: summary),
            if (pill != null) ...[const SizedBox(width: 8), pill],
          ],
        ),
      ),
    );
    return [
      for (final i in indices)
        row(
          Text('${i + 1}', style: context.mono(12.5, color: t.dim)),
          RuleSummary(_rules[i]),
          switch (_rules[i]) {
            FlatRule(:final target) => RouteTargetPill(target, nodes: nodes),
            OpaqueRule() => null,
          },
        ),
      if (withDefault)
        row(
          Icon(Icons.subdirectory_arrow_right, size: 15, color: t.faint),
          Text('default', style: context.mono(12, color: t.dim)),
          RouteTargetPill(_defaultTarget, nodes: nodes),
        ),
    ];
  }

  /// [child] as the anchor of a dropdown listing [rows]; a tap toggles it.
  Widget _menu(BuildContext context, List<Widget> rows, Widget child) =>
      MenuAnchor(
        menuChildren: rows,
        builder: (context, controller, child) => GestureDetector(
          behavior: HitTestBehavior.opaque,
          onTap: () =>
              controller.isOpen ? controller.close() : controller.open(),
          child: child,
        ),
        child: child,
      );
}

/// The static fan between "You" (or "Internet") and the lanes: one curve per
/// lane from the vertical centre to the lane's [ys] at the other edge, in its
/// [colors]. [out] = fans out to the right; false = fans in.
class _FanPainter extends CustomPainter {
  _FanPainter({required this.ys, required this.colors, required this.out});

  final List<double> ys;
  final List<Color> colors;
  final bool out;

  @override
  void paint(Canvas canvas, Size size) {
    final mid = size.height / 2;
    final w = size.width;
    for (var i = 0; i < ys.length; i++) {
      final (y0, y1) = out ? (mid, ys[i]) : (ys[i], mid);
      canvas.drawPath(
        Path()
          ..moveTo(0, y0)
          ..cubicTo(w / 2, y0, w / 2, y1, w, y1),
        Paint()
          ..color = colors[i].withValues(alpha: 0.5)
          ..style = PaintingStyle.stroke
          ..strokeWidth = 2,
      );
    }
  }

  @override
  bool shouldRepaint(_FanPainter old) =>
      old.out != out ||
      !listEquals(old.ys, ys) ||
      !listEquals(old.colors, colors);
}
