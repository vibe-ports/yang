// SPDX-License-Identifier: BSD-3-Clause AND BSD-3-Clause WITH PCRE2-exception
// Data from PCRE2 10.46 src/pcre2_ucptables.c (utt_names)
// (BSD-3-Clause WITH PCRE2-exception, © University of Cambridge; see NOTICE).

package xsdre

// pcreUCPNames are the property names PCRE2 10.46 accepts after \p and \P, lower case and without
// the ignorable characters, sorted (PRIV(utt_names)). Only the general categories, "l&"/"lc" and
// "any" are translated (pcreProperty); the others (scripts, binary properties, Bidi classes) are
// refused as unsupported, a name not listed is PCRE2's "unknown property" error.
var pcreUCPNames = []string{
	"adlam", "adlm", "aghb", "ahex", "ahom", "alpha", "alphabetic", "anatolianhieroglyphs", "any", "arab",
	"arabic", "armenian", "armi", "armn", "ascii", "asciihexdigit", "avestan", "avst", "bali", "balinese",
	"bamu", "bamum", "bass", "bassavah", "batak", "batk", "beng", "bengali", "bhaiksuki", "bhks",
	"bidial", "bidian", "bidib", "bidibn", "bidic", "bidicontrol", "bidics", "bidien", "bidies", "bidiet",
	"bidifsi", "bidil", "bidilre", "bidilri", "bidilro", "bidim", "bidimirrored", "bidinsm", "bidion", "bidipdf",
	"bidipdi", "bidir", "bidirle", "bidirli", "bidirlo", "bidis", "bidiws", "bopo", "bopomofo", "brah",
	"brahmi", "brai", "braille", "bugi", "buginese", "buhd", "buhid", "c", "cakm", "canadianaboriginal",
	"cans", "cari", "carian", "cased", "caseignorable", "caucasianalbanian", "cc", "cf", "chakma", "cham",
	"changeswhencasefolded", "changeswhencasemapped", "changeswhenlowercased", "changeswhentitlecased", "changeswhenuppercased", "cher", "cherokee", "chorasmian", "chrs", "ci",
	"cn", "co", "common", "copt", "coptic", "cpmn", "cprt", "cs", "cuneiform", "cwcf",
	"cwcm", "cwl", "cwt", "cwu", "cypriot", "cyprominoan", "cyrillic", "cyrl", "dash", "defaultignorablecodepoint",
	"dep", "deprecated", "deseret", "deva", "devanagari", "di", "dia", "diacritic", "diak", "divesakuru",
	"dogr", "dogra", "dsrt", "dupl", "duployan", "ebase", "ecomp", "egyp", "egyptianhieroglyphs", "elba",
	"elbasan", "elym", "elymaic", "emod", "emoji", "emojicomponent", "emojimodifier", "emojimodifierbase", "emojipresentation", "epres",
	"ethi", "ethiopic", "ext", "extendedpictographic", "extender", "extpict", "gara", "garay", "geor", "georgian",
	"glag", "glagolitic", "gong", "gonm", "goth", "gothic", "gran", "grantha", "graphemebase", "graphemeextend",
	"graphemelink", "grbase", "greek", "grek", "grext", "grlink", "gujarati", "gujr", "gukh", "gunjalagondi",
	"gurmukhi", "guru", "gurungkhema", "han", "hang", "hangul", "hani", "hanifirohingya", "hano", "hanunoo",
	"hatr", "hatran", "hebr", "hebrew", "hex", "hexdigit", "hira", "hiragana", "hluw", "hmng",
	"hmnp", "hung", "idc", "idcompatmathcontinue", "idcompatmathstart", "idcontinue", "ideo", "ideographic", "ids", "idsb",
	"idsbinaryoperator", "idst", "idstart", "idstrinaryoperator", "idsu", "idsunaryoperator", "imperialaramaic", "incb", "inherited", "inscriptionalpahlavi",
	"inscriptionalparthian", "ital", "java", "javanese", "joinc", "joincontrol", "kaithi", "kali", "kana", "kannada",
	"katakana", "kawi", "kayahli", "khar", "kharoshthi", "khitansmallscript", "khmer", "khmr", "khoj", "khojki",
	"khudawadi", "kiratrai", "kits", "knda", "krai", "kthi", "l", "l&", "lana", "lao",
	"laoo", "latin", "latn", "lc", "lepc", "lepcha", "limb", "limbu", "lina", "linb",
	"lineara", "linearb", "lisu", "ll", "lm", "lo", "loe", "logicalorderexception", "lower", "lowercase",
	"lt", "lu", "lyci", "lycian", "lydi", "lydian", "m", "mahajani", "mahj", "maka",
	"makasar", "malayalam", "mand", "mandaic", "mani", "manichaean", "marc", "marchen", "masaramgondi", "math",
	"mc", "mcm", "me", "medefaidrin", "medf", "meeteimayek", "mend", "mendekikakui", "merc", "mero",
	"meroiticcursive", "meroitichieroglyphs", "miao", "mlym", "mn", "modi", "modifiercombiningmark", "mong", "mongolian", "mro",
	"mroo", "mtei", "mult", "multani", "myanmar", "mymr", "n", "nabataean", "nagm", "nagmundari",
	"nand", "nandinagari", "narb", "nbat", "nchar", "nd", "newa", "newtailue", "nko", "nkoo",
	"nl", "no", "noncharactercodepoint", "nshu", "nushu", "nyiakengpuachuehmong", "ogam", "ogham", "olchiki", "olck",
	"oldhungarian", "olditalic", "oldnortharabian", "oldpermic", "oldpersian", "oldsogdian", "oldsoutharabian", "oldturkic", "olduyghur", "olonal",
	"onao", "oriya", "orkh", "orya", "osage", "osge", "osma", "osmanya", "ougr", "p",
	"pahawhhmong", "palm", "palmyrene", "patsyn", "patternsyntax", "patternwhitespace", "patws", "pauc", "paucinhau", "pc",
	"pcm", "pd", "pe", "perm", "pf", "phag", "phagspa", "phli", "phlp", "phnx",
	"phoenician", "pi", "plrd", "po", "prependedconcatenationmark", "prti", "ps", "psalterpahlavi", "qaac", "qaai",
	"qmark", "quotationmark", "radical", "regionalindicator", "rejang", "ri", "rjng", "rohg", "runic", "runr",
	"s", "samaritan", "samr", "sarb", "saur", "saurashtra", "sc", "sd", "sentenceterminal", "sgnw",
	"sharada", "shavian", "shaw", "shrd", "sidd", "siddham", "signwriting", "sind", "sinh", "sinhala",
	"sk", "sm", "so", "softdotted", "sogd", "sogdian", "sogo", "sora", "sorasompeng", "soyo",
	"soyombo", "space", "sterm", "sund", "sundanese", "sunu", "sunuwar", "sylo", "sylotinagri", "syrc",
	"syriac", "tagalog", "tagb", "tagbanwa", "taile", "taitham", "taiviet", "takr", "takri", "tale",
	"talu", "tamil", "taml", "tang", "tangsa", "tangut", "tavt", "telu", "telugu", "term",
	"terminalpunctuation", "tfng", "tglg", "thaa", "thaana", "thai", "tibetan", "tibt", "tifinagh", "tirh",
	"tirhuta", "tnsa", "todhri", "todr", "toto", "tulutigalari", "tutg", "ugar", "ugaritic", "uideo",
	"unifiedideograph", "unknown", "upper", "uppercase", "vai", "vaii", "variationselector", "vith", "vithkuqi", "vs",
	"wancho", "wara", "warangciti", "wcho", "whitespace", "wspace", "xan", "xidc", "xidcontinue", "xids",
	"xidstart", "xpeo", "xps", "xsp", "xsux", "xuc", "xwd", "yezi", "yezidi", "yi",
	"yiii", "z", "zanabazarsquare", "zanb", "zinh", "zl", "zp", "zs", "zyyy", "zzzz",
}
