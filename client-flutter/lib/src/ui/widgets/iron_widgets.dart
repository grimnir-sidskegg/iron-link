/// The shared, token-driven building blocks of the redesign. Every screen
/// composes its UI from these so surfaces, borders, radii, tags and controls
/// stay consistent and read their colours from [IronTheme] (`context.iron`) —
/// never a hard-coded hex.
///
/// Spec anchors: cards/panels radius 14–16, rows/chips 11–13, pills 999;
/// everything is outlined with a 1px `border` (no heavy shadows inside the app);
/// surfaces separate by the `panel`→`raised` step + the border, not elevation.
library;

import 'package:flutter/material.dart';

import '../theme/app_colors.dart';

/// An outlined surface — the workhorse container for cards, panels and rows.
/// Fills with [raised] (one step above `panel`) by default; pass `tinted` for a
/// soft accent/colour wash. Optional [onTap] makes it an inkable row.
class IronCard extends StatelessWidget {
  const IronCard({
    super.key,
    required this.child,
    this.padding = const EdgeInsets.all(16),
    this.radius = 16,
    this.color,
    this.border,
    this.onTap,
    this.tinted,
  });

  final Widget child;
  final EdgeInsetsGeometry padding;
  final double radius;

  /// Surface fill. Defaults to `raised`.
  final Color? color;

  /// Border colour. Defaults to the `border` token.
  final Color? border;

  /// When set, the card fills with this colour at low alpha and outlines in it
  /// — the "tinted card" used for the Doctor summary / problem rows.
  final Color? tinted;

  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final fill = tinted != null
        ? Color.alphaBlend(tinted!.withValues(alpha: 0.10), t.panel)
        : (color ?? t.raised);
    final line = tinted != null
        ? tinted!.withValues(alpha: 0.35)
        : (border ?? t.border);
    final radii = BorderRadius.circular(radius);
    final content = Padding(padding: padding, child: child);
    return DecoratedBox(
      decoration: BoxDecoration(
        color: fill,
        borderRadius: radii,
        border: Border.all(color: line, width: 1),
      ),
      child: onTap == null
          ? content
          : Material(
              type: MaterialType.transparency,
              child: InkWell(
                  onTap: onTap, borderRadius: radii, child: content),
            ),
    );
  }
}

/// A 1px hairline in the `border` token — separates rows inside a group.
class IronDivider extends StatelessWidget {
  const IronDivider({super.key, this.indent = 0});
  final double indent;

  @override
  Widget build(BuildContext context) => Divider(
      height: 1, thickness: 1, indent: indent, color: context.iron.border);
}

/// A small status / action tag — a tinted pill of `colour@.14` filled, with the
/// label in [colour] (or its strong variant). Used for OK / NOTICE / Block /
/// Direct / proxy-node tags and uppercase eyebrows.
class IronTag extends StatelessWidget {
  const IronTag(
    this.label, {
    super.key,
    required this.color,
    this.icon,
    this.uppercase = false,
  });

  final String label;
  final Color color;
  final IconData? icon;
  final bool uppercase;

  @override
  Widget build(BuildContext context) {
    final text = uppercase ? label.toUpperCase() : label;
    return Container(
      padding: EdgeInsets.symmetric(horizontal: icon == null ? 10 : 8, vertical: 4),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.14),
        borderRadius: BorderRadius.circular(999),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          if (icon != null) ...[
            Icon(icon, size: 13, color: color),
            const SizedBox(width: 5),
          ],
          Text(
            text,
            style: TextStyle(
              color: color,
              fontWeight: FontWeight.w700,
              fontSize: 11.5,
              letterSpacing: uppercase ? 0.6 : 0.1,
            ),
          ),
        ],
      ),
    );
  }
}

/// The protocol / transport chip on node rows — a quiet `raised`-on-`panel`
/// pill with `dim` text. Pass `accent` to tint it (e.g. a core pin).
class IronKindChip extends StatelessWidget {
  const IronKindChip(this.label, {super.key, this.accent = false});

  final String label;
  final bool accent;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 2),
      decoration: BoxDecoration(
        color: accent ? t.accentSoft : t.raised,
        borderRadius: BorderRadius.circular(6),
        border: Border.all(color: accent ? t.accent.withValues(alpha: 0.3) : t.border),
      ),
      child: Text(
        label,
        style: TextStyle(
          fontSize: 10.5,
          fontWeight: FontWeight.w600,
          color: accent ? t.accentStrong : t.dim,
        ),
      ),
    );
  }
}

/// A small rounded icon tile — the leading affordance on rule / check / list
/// rows. Size 38, radius 11, filled `colour@.13`, icon in [color].
class IronIconTile extends StatelessWidget {
  const IronIconTile(this.icon,
      {super.key, required this.color, this.size = 38});

  final IconData icon;
  final Color color;
  final double size;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: size,
      height: size,
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.13),
        borderRadius: BorderRadius.circular(11),
      ),
      child: Icon(icon, size: size * 0.5, color: color),
    );
  }
}

/// The redesigned segmented control: a `raised` pill track of equal segments;
/// the selected one fills `accentSoft` with `accentStrong` text, the rest are
/// transparent with `dim` text. Generic over the value type.
class IronSegmented<T> extends StatelessWidget {
  const IronSegmented({
    super.key,
    required this.segments,
    required this.selected,
    required this.onChanged,
  });

  /// (value, label) pairs in display order.
  final List<(T, String)> segments;
  final T selected;
  final ValueChanged<T> onChanged;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    return Container(
      padding: const EdgeInsets.all(3),
      decoration: BoxDecoration(
        color: t.raised,
        borderRadius: BorderRadius.circular(999),
        border: Border.all(color: t.border),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          for (final (value, label) in segments)
            GestureDetector(
              onTap: () => onChanged(value),
              child: AnimatedContainer(
                duration: const Duration(milliseconds: 180),
                padding:
                    const EdgeInsets.symmetric(horizontal: 16, vertical: 7),
                decoration: BoxDecoration(
                  color: value == selected ? t.accentSoft : Colors.transparent,
                  borderRadius: BorderRadius.circular(999),
                ),
                child: Text(
                  label,
                  style: TextStyle(
                    fontSize: 12.5,
                    fontWeight: FontWeight.w600,
                    color: value == selected ? t.accentStrong : t.dim,
                  ),
                ),
              ),
            ),
        ],
      ),
    );
  }
}

/// The redesigned pill switch — 42×24, 20px white knob. On = `accent` track,
/// knob right; off = `border` track, knob left. Animated 200ms. A drop-in for
/// the data-bound toggles across Settings / Routing.
class IronSwitch extends StatelessWidget {
  const IronSwitch({super.key, required this.value, required this.onChanged});

  final bool value;
  final ValueChanged<bool>? onChanged;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final enabled = onChanged != null;
    return GestureDetector(
      onTap: enabled ? () => onChanged!(!value) : null,
      child: Opacity(
        opacity: enabled ? 1 : 0.5,
        child: AnimatedContainer(
          duration: const Duration(milliseconds: 200),
          curve: Curves.easeOut,
          width: 42,
          height: 24,
          padding: const EdgeInsets.all(2),
          decoration: BoxDecoration(
            color: value ? t.accent : t.border,
            borderRadius: BorderRadius.circular(999),
          ),
          child: AnimatedAlign(
            duration: const Duration(milliseconds: 200),
            curve: Curves.easeOut,
            alignment: value ? Alignment.centerRight : Alignment.centerLeft,
            child: Container(
              width: 20,
              height: 20,
              decoration: const BoxDecoration(
                color: Color(0xFFFFFFFF),
                shape: BoxShape.circle,
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// A grouped-settings section: an uppercase `faint` header above a single
/// `raised` rounded card; [rows] are stacked and split by a 1px `border`
/// divider. The Settings screen is a column of these.
class IronSectionGroup extends StatelessWidget {
  const IronSectionGroup({
    super.key,
    required this.title,
    required this.rows,
    this.trailing,
  });

  final String title;
  final List<Widget> rows;

  /// Optional trailing widget on the header line (e.g. a section action).
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(4, 0, 4, 9),
          child: Row(
            children: [
              Text(
                title.toUpperCase(),
                style: TextStyle(
                  color: t.faint,
                  fontSize: 11,
                  fontWeight: FontWeight.w700,
                  letterSpacing: 0.8,
                ),
              ),
              const Spacer(),
              ?trailing,
            ],
          ),
        ),
        IronCard(
          padding: EdgeInsets.zero,
          child: Column(
            children: [
              for (var i = 0; i < rows.length; i++) ...[
                if (i > 0) const IronDivider(indent: 16),
                rows[i],
              ],
            ],
          ),
        ),
      ],
    );
  }
}

/// One row inside an [IronSectionGroup]: a title (+ optional subtitle) on the
/// left and a [trailing] control on the right.
class IronSettingRow extends StatelessWidget {
  const IronSettingRow({
    super.key,
    required this.title,
    this.subtitle,
    this.trailing,
    this.leading,
  });

  final String title;
  final String? subtitle;
  final Widget? trailing;
  final Widget? leading;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.center,
        children: [
          if (leading != null) ...[leading!, const SizedBox(width: 12)],
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              mainAxisSize: MainAxisSize.min,
              children: [
                Text(title,
                    style: const TextStyle(
                        fontSize: 14, fontWeight: FontWeight.w600)),
                if (subtitle != null)
                  Padding(
                    padding: const EdgeInsets.only(top: 3),
                    child: Text(subtitle!,
                        style: TextStyle(
                            fontSize: 12, height: 1.35, color: t.dim)),
                  ),
              ],
            ),
          ),
          if (trailing != null) ...[const SizedBox(width: 12), trailing!],
        ],
      ),
    );
  }
}

/// A compact outlined button in the design language — `raised` fill, `border`
/// outline, radius 11, optional leading icon. The neutral action button (Pause,
/// Clear, Reload, …). Pass [accent] to make it the accent-outline variant
/// (Run checks / Re-test).
class IronButton extends StatelessWidget {
  const IronButton({
    super.key,
    required this.label,
    this.icon,
    this.onPressed,
    this.accent = false,
    this.danger = false,
  });

  final String label;
  final IconData? icon;
  final VoidCallback? onPressed;
  final bool accent;
  final bool danger;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final enabled = onPressed != null;
    final fg = danger
        ? t.danger
        : accent
            ? t.accentStrong
            : t.text;
    final fill = accent || danger ? Colors.transparent : t.raised;
    final line = danger
        ? t.danger.withValues(alpha: 0.5)
        : accent
            ? t.accent.withValues(alpha: 0.5)
            : t.border;
    final radii = BorderRadius.circular(11);
    return Opacity(
      opacity: enabled ? 1 : 0.45,
      child: Material(
        type: MaterialType.transparency,
        child: InkWell(
          onTap: onPressed,
          borderRadius: radii,
          child: Container(
            padding: EdgeInsets.symmetric(
                horizontal: icon == null ? 16 : 13, vertical: 9),
            decoration: BoxDecoration(
              color: fill,
              borderRadius: radii,
              border: Border.all(color: line),
            ),
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                if (icon != null) ...[
                  Icon(icon, size: 16, color: fg),
                  const SizedBox(width: 7),
                ],
                Text(label,
                    style: TextStyle(
                        fontSize: 13, fontWeight: FontWeight.w600, color: fg)),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

/// A tiny circular icon-only button (refresh / clear / overflow) — `raised`
/// disc with `dim` icon. For the inline header actions.
class IronIconButton extends StatelessWidget {
  const IronIconButton({
    super.key,
    required this.icon,
    this.onPressed,
    this.tooltip,
    this.color,
  });

  final IconData icon;
  final VoidCallback? onPressed;
  final String? tooltip;
  final Color? color;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final button = Material(
      color: t.raised,
      shape: CircleBorder(side: BorderSide(color: t.border)),
      clipBehavior: Clip.antiAlias,
      child: InkWell(
        onTap: onPressed,
        child: Padding(
          padding: const EdgeInsets.all(9),
          child: Icon(icon, size: 18, color: color ?? t.dim),
        ),
      ),
    );
    return tooltip == null ? button : Tooltip(message: tooltip!, child: button);
  }
}
