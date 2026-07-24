/// The redesign's motion primitives — the looping ambient animations from the
/// spec's motion table, each gated by an [animate] flag so a caller can honour
/// "Reduce motion" (and the connected/disconnected desaturation) by passing
/// `false`, which renders the calm static form.
///
///  * [PulseRing]    expanding fading ring (power orb + active node)
///  * [BreathingScale] gentle scale 1↔1.03 (power orb inner disc)
///  * [PipelineThread] the dashed accent thread + traveling glow dot (You→…→Internet)
library;

import 'dart:math' as math;

import 'package:flutter/material.dart';

/// An expanding, fading ring that loops behind [child] — `scale 1→1.7`,
/// `opacity .55→0`, ~2.7s ease-out. When [animate] is false it draws nothing
/// (just the child), so a disconnected / reduce-motion orb is still.
class PulseRing extends StatefulWidget {
  const PulseRing({
    super.key,
    required this.animate,
    required this.color,
    required this.child,
    this.maxScale = 1.7,
    this.duration = const Duration(milliseconds: 2700),
  });

  final bool animate;
  final Color color;
  final Widget child;
  final double maxScale;
  final Duration duration;

  @override
  State<PulseRing> createState() => _PulseRingState();
}

class _PulseRingState extends State<PulseRing>
    with SingleTickerProviderStateMixin {
  late final AnimationController _c =
      AnimationController(vsync: this, duration: widget.duration);

  @override
  void initState() {
    super.initState();
    if (widget.animate) _c.repeat();
  }

  @override
  void didUpdateWidget(PulseRing old) {
    super.didUpdateWidget(old);
    if (widget.animate && !_c.isAnimating) {
      _c.repeat();
    } else if (!widget.animate && _c.isAnimating) {
      _c.stop();
      _c.value = 0;
    }
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    // The Stack sizes to [child]; the ring fills that footprint and scales out
    // past it. The Positioned.fill MUST be a direct child of the Stack — the
    // scaling Transform/Opacity live *inside* it, never wrapping it (else the
    // Positioned lands on an Opacity and Flutter throws a ParentData error).
    return Stack(
      alignment: Alignment.center,
      clipBehavior: Clip.none,
      children: [
        if (widget.animate)
          Positioned.fill(
            child: IgnorePointer(
              child: AnimatedBuilder(
                animation: _c,
                builder: (context, _) {
                  final t = Curves.easeOut.transform(_c.value);
                  return Transform.scale(
                    scale: 1 + (widget.maxScale - 1) * t,
                    child: Opacity(
                      opacity: (0.55 * (1 - t)).clamp(0.0, 1.0),
                      child: DecoratedBox(
                        decoration: BoxDecoration(
                          shape: BoxShape.circle,
                          border: Border.all(color: widget.color, width: 2),
                        ),
                      ),
                    ),
                  );
                },
              ),
            ),
          ),
        widget.child,
      ],
    );
  }
}

/// Wraps [child] in a gentle, looping scale (1↔[amount], 4s ease-in-out) — the
/// "breathing" power disc. [animate] false renders the child at rest.
class BreathingScale extends StatefulWidget {
  const BreathingScale({
    super.key,
    required this.animate,
    required this.child,
    this.amount = 1.03,
    this.duration = const Duration(seconds: 4),
  });

  final bool animate;
  final Widget child;
  final double amount;
  final Duration duration;

  @override
  State<BreathingScale> createState() => _BreathingScaleState();
}

class _BreathingScaleState extends State<BreathingScale>
    with SingleTickerProviderStateMixin {
  late final AnimationController _c =
      AnimationController(vsync: this, duration: widget.duration);

  @override
  void initState() {
    super.initState();
    if (widget.animate) _c.repeat(reverse: true);
  }

  @override
  void didUpdateWidget(BreathingScale old) {
    super.didUpdateWidget(old);
    if (widget.animate && !_c.isAnimating) {
      _c.repeat(reverse: true);
    } else if (!widget.animate && _c.isAnimating) {
      _c.stop();
      _c.value = 0;
    }
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    if (!widget.animate) return widget.child;
    return AnimatedBuilder(
      animation: _c,
      builder: (context, child) {
        final t = Curves.easeInOut.transform(_c.value);
        return Transform.scale(scale: 1 + (widget.amount - 1) * t, child: child);
      },
      child: widget.child,
    );
  }
}

/// The signature pipeline connector: a horizontal thread that fills its width.
///
///  * [animate] true  → accent dashes scrolling left→right (`flow`) with a
///    glowing dot traveling along it (`dotmove`).
///  * [animate] false → a flat, static [idle] line, no dot (disconnected /
///    reduce-motion).
///
/// Expands to the available width; give it a bounded width (e.g. `Expanded`).
class PipelineThread extends StatefulWidget {
  const PipelineThread({
    super.key,
    required this.animate,
    required this.accent,
    required this.idle,
    this.height = 22,
  });

  final bool animate;
  final Color accent;
  final Color idle;
  final double height;

  @override
  State<PipelineThread> createState() => _PipelineThreadState();
}

class _PipelineThreadState extends State<PipelineThread>
    with SingleTickerProviderStateMixin {
  late final AnimationController _c = AnimationController(
      vsync: this, duration: const Duration(milliseconds: 2400));

  @override
  void initState() {
    super.initState();
    if (widget.animate) _c.repeat();
  }

  @override
  void didUpdateWidget(PipelineThread old) {
    super.didUpdateWidget(old);
    if (widget.animate && !_c.isAnimating) {
      _c.repeat();
    } else if (!widget.animate && _c.isAnimating) {
      _c.stop();
      _c.value = 0;
    }
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      height: widget.height,
      width: double.infinity,
      child: AnimatedBuilder(
        animation: _c,
        builder: (context, _) => CustomPaint(
          painter: _ThreadPainter(
            phase: _c.value,
            animate: widget.animate,
            accent: widget.accent,
            idle: widget.idle,
          ),
          size: Size.infinite,
        ),
      ),
    );
  }
}

class _ThreadPainter extends CustomPainter {
  _ThreadPainter({
    required this.phase,
    required this.animate,
    required this.accent,
    required this.idle,
  });

  final double phase; // 0..1
  final bool animate;
  final Color accent;
  final Color idle;

  static const _dash = 8.0;
  static const _gap = 8.0;

  @override
  void paint(Canvas canvas, Size size) {
    final y = size.height / 2;
    if (!animate) {
      final paint = Paint()
        ..color = idle
        ..strokeWidth = 2
        ..strokeCap = StrokeCap.round;
      canvas.drawLine(Offset(0, y), Offset(size.width, y), paint);
      return;
    }

    // Dashes scrolling left→right (the dash phase runs 3× the dot period so the
    // thread reads as ~0.8s, the dot as ~2.4s).
    final dashPaint = Paint()
      ..color = accent
      ..strokeWidth = 4
      ..strokeCap = StrokeCap.round;
    final period = _dash + _gap;
    final offset = (phase * 3.0 % 1.0) * period;
    for (double x = -period + offset; x < size.width; x += period) {
      final start = math.max(0.0, x);
      final end = math.min(size.width, x + _dash);
      if (end > start) {
        canvas.drawLine(Offset(start, y), Offset(end, y), dashPaint);
      }
    }

    // The traveling glow dot — fades in/out at the ends.
    final dx = phase * size.width;
    final edge = (math.min(dx, size.width - dx) / 24).clamp(0.0, 1.0);
    if (edge > 0) {
      canvas.drawCircle(
        Offset(dx, y),
        7,
        Paint()
          ..color = accent.withValues(alpha: 0.30 * edge)
          ..maskFilter = const MaskFilter.blur(BlurStyle.normal, 6),
      );
      canvas.drawCircle(
        Offset(dx, y),
        3.5,
        Paint()..color = accent.withValues(alpha: edge),
      );
    }
  }

  @override
  bool shouldRepaint(_ThreadPainter old) =>
      old.phase != phase ||
      old.animate != animate ||
      old.accent != accent ||
      old.idle != idle;
}
