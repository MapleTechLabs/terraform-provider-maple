// The publish-script of .github/workflows/release.yml. It tags the version in package.json
// when that tag does not exist yet and reports it through CHANGESETS_OUTPUT, so
// changesets/action pushes the tag and cuts the GitHub release. The release jobs then build
// and publish from that tag.
import { execFileSync } from "node:child_process"
import { appendFileSync, readFileSync } from "node:fs"

const { name, version } = JSON.parse(readFileSync("package.json", "utf8"))
const tag = `v${version}`
const git = (...args) => execFileSync("git", args, { encoding: "utf8" }).trim()

// changesets/action runs this on every push without pending changesets; 0.0.0 means nothing
// has been versioned yet.
if (version === "0.0.0") {
    console.log("Nothing versioned yet.")
} else if (git("ls-remote", "--tags", "origin", `refs/tags/${tag}`) !== "") {
    console.log(`${tag} is already tagged.`)
} else {
    git("tag", "-a", tag, "-m", tag)
    console.log(`New tag: ${tag}`)
    if (process.env.CHANGESETS_OUTPUT) {
        appendFileSync(process.env.CHANGESETS_OUTPUT, `${JSON.stringify({ type: "git-tag", tag, packageName: name })}\n`)
    }
}
